package cluster

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azare242/gobank/internal/auth"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/limiter"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	"golang.org/x/crypto/bcrypt"
)

// App provides a deliberately separate cluster API; single-node handlers never
// get a reference to this database and cannot bypass the replicated log.
func (n *Node) App(tokens *auth.Service, credentialKey []byte) *fiber.App {
	app := fiber.New(fiber.Config{Immutable: true, BodyLimit: 16 * 1024, ErrorHandler: func(c fiber.Ctx, err error) error {
		status := 500
		code := "internal_error"
		message := "Internal server error"
		var e *fiber.Error
		if errors.As(err, &e) {
			status = e.Code
			message = e.Message
			switch status {
			case 400, 413:
				code = "malformed_request"
			case 401:
				code = "unauthorized"
			case 404, 405:
				code = "unsupported_action"
			case 429:
				code = "server_busy"
			}
		}
		return send(c, reject(status, code, message))
	}})
	slots := make(chan struct{}, n.Config.MaxConcurrent)
	app.Use(func(c fiber.Ctx) error {
		start := time.Now()
		rid := c.Get("X-Request-ID")
		if rid == "" {
			rid = uuid.NewString()
		}
		if len(rid) > 128 {
			rid = uuid.NewString()
		}
		c.Set("X-Request-ID", rid)
		c.Locals("request_id", rid)
		c.Set("X-Bank-Node", n.Config.ID)
		c.Set("Cache-Control", "no-store")
		err := c.Next()
		if err != nil {
			_ = c.App().Config().ErrorHandler(c, err)
		}
		parts := strings.Split(strings.Trim(c.Path(), "/"), "/")
		action, account := c.Method(), ""
		if len(parts) == 3 && parts[0] == "accounts" {
			account = parts[1]
			action = parts[2]
		}
		log.Printf("timestamp=%s node=%q request_id=%q operation_id=%q client_ip=%q action=%q account=%q status=%d latency_ms=%.3f response=%s", start.UTC().Format(time.RFC3339Nano), n.Config.ID, rid, c.Get("Idempotency-Key"), c.IP(), action, account, c.Response().StatusCode(), float64(time.Since(start).Microseconds())/1000, safeResponse(c))
		return nil
	}, recover.New(), func(c fiber.Ctx) error {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			return c.Next()
		default:
			return send(c, reject(429, "server_busy", "Concurrency limit reached"))
		}
	})
	app.Get("/health", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok", "node": n.Config.ID}) })
	app.Get("/cluster/status", func(c fiber.Ctx) error {
		digest, index, err := n.FSM.Digest()
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"node": n.Config.ID, "role": n.Raft.State().String(), "leader": n.LeaderHTTP(), "applied_index": index, "state_digest": digest, "diagnostic_only": true, "raft": n.Raft.Stats()})
	})
	command := func(c fiber.Ctx, action string) Command {
		return Command{Action: action, Timestamp: time.Now().UTC().Format(time.RFC3339Nano), ClientIP: c.IP(), RequestID: c.Locals("request_id").(string)}
	}
	submit := func(c fiber.Ctx, cmd Command) error {
		r, err := n.Submit(cmd)
		if err != nil {
			return n.unavailable(c, err)
		}
		if n.Config.EnableDemoFaults && c.Get("X-Demo-Crash-After-Commit") == "true" && r.Status == "success" && (cmd.Action == "deposit" || cmd.Action == "withdraw") {
			log.Printf("DEMO crash after committed operation_id=%q; no response sent", cmd.ID)
			os.Exit(86)
		}
		return send(c, r)
	}
	// A request must commit a fence before reading login/profile data. No follower
	// or isolated former leader serves authoritative reads from a stale DB.
	fence := func(c fiber.Ctx) error { _, err := n.Submit(command(c, "barrier")); return err }
	app.Get("/ready", func(c fiber.Ctx) error {
		if err := fence(c); err != nil {
			return n.unavailable(c, err)
		}
		return c.JSON(fiber.Map{"status": "ready"})
	})
	app.Post("/users", func(c fiber.Ctx) error {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.Unmarshal(c.Body(), &in) != nil {
			return send(c, reject(400, "malformed_request", "Invalid JSON"))
		}
		in.Username = strings.TrimSpace(in.Username)
		if in.Username == "" || in.Password == "" {
			return send(c, reject(400, "missing_field", "Username and password required"))
		}
		if utf8.RuneCountInString(in.Username) > 64 || len(in.Password) < 8 || len(in.Password) > 72 {
			return send(c, reject(400, "malformed_request", "Invalid username or password length"))
		}
		id := c.Get("Idempotency-Key")
		if !validID(id) {
			return send(c, reject(400, "missing_field", "Idempotency-Key (1-128 characters) required"))
		}
		if err := fence(c); err != nil {
			return n.unavailable(c, err)
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, credentialKey)
		b, _ := json.Marshal(in)
		mac.Write(b)
		cmd := command(c, "register")
		cmd.ID = id
		cmd.Username = in.Username
		cmd.PasswordHash = string(hash)
		cmd.Fingerprint = hex.EncodeToString(mac.Sum(nil))
		return submit(c, cmd)
	})
	dummy, _ := bcrypt.GenerateFromPassword([]byte("dummy-login-password"), bcrypt.DefaultCost)
	app.Post("/login", limiter.New(limiter.Config{Max: 10, Expiration: time.Minute}), func(c fiber.Ctx) error {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if json.Unmarshal(c.Body(), &in) != nil {
			return send(c, reject(400, "malformed_request", "Invalid JSON"))
		}
		if err := fence(c); err != nil {
			return n.unavailable(c, err)
		}
		s, err := n.FSM.read()
		if err != nil {
			return err
		}
		u, ok := s.Users[strings.TrimSpace(in.Username)]
		hash := dummy
		if ok {
			hash = []byte(u.PasswordHash)
		}
		comparison := bcrypt.CompareHashAndPassword(hash, []byte(in.Password))
		cmd := command(c, "login")
		cmd.Username = strings.TrimSpace(in.Username)
		if len(cmd.Username) > 64 {
			cmd.Username = ""
		}
		cmd.LoginOK = comparison == nil && ok
		if cmd.LoginOK {
			cmd.UserID = u.ID
		}
		audited, err := n.Submit(cmd)
		if err != nil {
			return n.unavailable(c, err)
		}
		if audited.HTTP != 200 {
			return send(c, audited)
		}
		token, err := tokens.Generate(u.ID)
		if err != nil {
			return err
		}
		return c.JSON(fiber.Map{"access_token": token, "token_type": "Bearer", "expires_in": int(auth.AccessTTL.Seconds()), "account": u.Account})
	})
	private := app.Group("", tokens.Middleware())
	private.Post("/cluster/transfer/:id", func(c fiber.Ctx) error {
		if !n.Config.EnableDemoFaults {
			return send(c, reject(404, "unsupported_action", "Demo controls disabled"))
		}
		if n.Raft.State().String() != "Leader" {
			return n.unavailable(c, fmt.Errorf("not leader"))
		}
		for _, p := range n.Config.Peers {
			if p.ID == c.Params("id") {
				if p.ID == n.Config.ID {
					return c.JSON(fiber.Map{"status": "success"})
				}
				if err := n.Raft.LeadershipTransferToServer(raft.ServerID(p.ID), raft.ServerAddress(p.Raft)).Error(); err != nil {
					return n.unavailable(c, err)
				}
				return c.JSON(fiber.Map{"status": "success", "leader": p.HTTP})
			}
		}
		return send(c, reject(404, "unknown_node", "Unknown peer"))
	})
	private.Post("/cluster/snapshot", func(c fiber.Ctx) error {
		if !n.Config.EnableDemoFaults {
			return send(c, reject(404, "unsupported_action", "Demo controls disabled"))
		}
		if err := fence(c); err != nil {
			return n.unavailable(c, err)
		}
		if err := n.Raft.Snapshot().Error(); err != nil {
			return err
		}
		return c.JSON(fiber.Map{"status": "success"})
	})
	private.Get("/me", func(c fiber.Ctx) error {
		if err := fence(c); err != nil {
			return n.unavailable(c, err)
		}
		uid, _ := auth.UserID(c)
		s, err := n.FSM.read()
		if err != nil {
			return err
		}
		for _, u := range s.Users {
			if u.ID == uid {
				return c.JSON(fiber.Map{"id": u.ID, "username": u.Username, "account": u.Account})
			}
		}
		return send(c, reject(401, "unauthorized", "User no longer exists"))
	})
	private.Get("/operations/:id", func(c fiber.Ctx) error {
		cmd := command(c, "operation")
		cmd.UserID, _ = auth.UserID(c)
		cmd.ID = c.Params("id")
		return submit(c, cmd)
	})
	private.Get("/accounts/:id/balance", func(c fiber.Ctx) error {
		cmd := command(c, "balance")
		cmd.UserID, _ = auth.UserID(c)
		cmd.Account = c.Params("id")
		return submit(c, cmd)
	})
	for _, action := range []string{"deposit", "withdraw"} {
		private.Post("/accounts/:id/"+action, func(c fiber.Ctx) error {
			var in map[string]json.RawMessage
			if json.Unmarshal(c.Body(), &in) != nil || in == nil {
				return send(c, reject(400, "malformed_request", "Invalid JSON"))
			}
			raw, ok := in["amount"]
			if !ok {
				return send(c, reject(400, "missing_field", "Amount required"))
			}
			var amount int64
			if json.Unmarshal(raw, &amount) != nil || amount <= 0 {
				return send(c, reject(400, "invalid_amount", "Amount must be a positive int64"))
			}
			id := c.Get("Idempotency-Key")
			if !validID(id) {
				return send(c, reject(400, "missing_field", "Idempotency-Key (1-128 characters) required"))
			}
			cmd := command(c, action)
			cmd.ID = id
			cmd.UserID, _ = auth.UserID(c)
			cmd.Account = c.Params("id")
			cmd.Amount = amount
			cmd.FailBeforeCommit = n.Config.EnableDemoFaults && c.Get("X-Demo-Fail-Before-Commit") == "true"
			return submit(c, cmd)
		})
	}
	private.All("/accounts/:id/:action", func(c fiber.Ctx) error {
		return send(c, reject(400, "unsupported_action", "Unsupported account action"))
	})
	return app
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func send(c fiber.Ctx, r Result) error {
	if r.HTTP == 0 {
		r.HTTP = 500
	}
	return c.Status(r.HTTP).JSON(r)
}
func (n *Node) unavailable(c fiber.Ctx, err error) error {
	log.Printf("node=%q temporarily unavailable: %v", n.Config.ID, err)
	return c.Status(503).JSON(fiber.Map{"status": "error", "error": "unavailable", "message": "Outcome may be unknown; retry same operation ID", "leader": n.LeaderHTTP()})
}
func safeResponse(c fiber.Ctx) string {
	// Login response contains a bearer token; never log it. Other responses
	// contain no passwords or hashes. JSON encoding quotes control characters.
	if c.Path() == "/login" {
		return `"login response omitted"`
	}
	b, _ := json.Marshal(string(c.Response().Body()))
	return string(b)
}
