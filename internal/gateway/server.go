package gateway

import (
	_ "embed"
	"errors"
	"fmt"
	"net/http"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/coordinator"
	"github.com/azare242/gobank/internal/httpbank"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/azare242/gobank/internal/shardmap"
	"golang.org/x/crypto/bcrypt"
)

//go:embed openapi.json
var specification []byte

type Server struct {
	Pool                *Pool
	Coordinator         *coordinator.Coordinator
	Tokens              *auth.Service
	FixturePasswordHash []byte
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(specification)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		httpbank.JSON(w, 200, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if !s.Pool.Ready() {
			httpbank.Error(w, serviceerr.New(503, "shard_unavailable"))
			return
		}
		if e := s.Coordinator.DB.PingContext(r.Context()); e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, map[string]string{"status": "ready"})
	})
	// Assignment 2 uses deterministic fixtures; public signup is deliberately absent.
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e := httpbank.Decode(w, r, &body); e != nil {
			httpbank.Error(w, e)
			return
		}
		n, e := shardmap.Number(body.Username)
		if e != nil || bcrypt.CompareHashAndPassword(s.FixturePasswordHash, []byte(body.Password)) != nil {
			httpbank.Error(w, serviceerr.New(401, "unauthorized"))
			return
		}
		token, e := s.Tokens.Generate(uint(n))
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		httpbank.JSON(w, 200, map[string]any{"access_token": token, "account": body.Username, "id": n})
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		user, e := httpbank.Identity(s.Tokens, r)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		if user > 2000 {
			httpbank.Error(w, serviceerr.New(404, "unknown_user"))
			return
		}
		httpbank.JSON(w, 200, map[string]any{"id": user, "account": fmt.Sprintf("A%04d", user)})
	})
	mux.HandleFunc("GET /accounts/{id}/balance", func(w http.ResponseWriter, r *http.Request) {
		user, e := httpbank.Identity(s.Tokens, r)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		account := r.PathValue("id")
		shard, e := s.Pool.Map.Owner(account)
		if e != nil {
			httpbank.Error(w, serviceerr.New(400, "invalid_account"))
			return
		}
		var out repository.Outcome
		rep, e := s.Pool.Call(r.Context(), shard.ID, "/internal/balance", map[string]any{"user_id": user, "account": account}, &out)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		w.Header().Set("X-Bank-Replica", rep)
		httpbank.JSON(w, 200, out)
	})
	mutate := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			user, e := httpbank.Identity(s.Tokens, r)
			if e != nil {
				httpbank.Error(w, e)
				return
			}
			var body struct {
				Source      string `json:"source"`
				Destination string `json:"destination"`
				Amount      int64  `json:"amount"`
			}
			if action == "transfer" {
				e = httpbank.Decode(w, r, &body)
			} else {
				var amount struct {
					Amount int64 `json:"amount"`
				}
				e = httpbank.Decode(w, r, &amount)
				body.Amount = amount.Amount
			}
			if e != nil {
				httpbank.Error(w, e)
				return
			}
			source := r.PathValue("id")
			if action == "transfer" {
				source = body.Source
			}
			m := repository.Mutation{UserID: user, Key: r.Header.Get("Idempotency-Key"), Action: action, Source: source, Amount: body.Amount}
			if action == "transfer" {
				m.Destination = body.Destination
			}
			rec, e := s.Coordinator.Process(r.Context(), m)
			w.Header().Set("X-Transaction-ID", rec.ID)
			if e != nil {
				var app *serviceerr.Error
				if rec.ID == "" || (errors.As(e, &app) && app.Status < 500) {
					httpbank.Error(w, e)
					return
				}
				httpbank.JSON(w, 202, repository.Outcome{Status: "pending", Pending: true, TransactionID: rec.ID, Decision: rec.Decision})
				return
			}
			w.Header().Set("X-Bank-Replica", rec.Replica)
			w.Header().Set("X-Transaction-ID", rec.ID)
			if rec.Pending {
				httpbank.JSON(w, 202, repository.Outcome{Status: "pending", Pending: true, TransactionID: rec.ID, Decision: rec.Decision})
				return
			}
			code := 200
			if rec.Outcome.Status == "error" {
				code = 409
			}
			httpbank.JSON(w, code, rec.Outcome)
		}
	}
	mux.HandleFunc("POST /accounts/{id}/deposit", mutate("deposit"))
	mux.HandleFunc("POST /accounts/{id}/withdraw", mutate("withdraw"))
	mux.HandleFunc("POST /transfers", mutate("transfer"))
	mux.HandleFunc("GET /transactions/{id}", func(w http.ResponseWriter, r *http.Request) {
		user, e := httpbank.Identity(s.Tokens, r)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		rec, e := s.Coordinator.Get(r.Context(), r.PathValue("id"), user)
		if e != nil {
			httpbank.Error(w, e)
			return
		}
		w.Header().Set("X-Transaction-ID", rec.ID)
		httpbank.JSON(w, 200, rec)
	})
	return httpbank.Log("", "", mux)
}
