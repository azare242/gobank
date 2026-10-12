package socketserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/service"
	"github.com/google/uuid"
)

func errorResponse(id string, err error) Response {
	r := Response{Version: 1, RequestID: id, Status: "error", StatusCode: 500, Error: "internal_error", Message: "Internal server error"}
	var e *service.Error
	if errors.As(err, &e) {
		r.StatusCode = e.Status
		r.Error = e.Code
		r.Message = e.Message
	}
	return r
}
func (s *Server) process(data []byte, ip string) (response Response) {
	start := time.Now()
	request, decodeErr := decode(data)
	id := request.RequestID
	if decodeErr != nil || id == "" {
		id = uuid.NewString()
	}
	request.RequestID = id
	audit := models.AuditLog{CreatedAt: start.UTC(), RequestID: id, ClientIP: ip, Method: "TCP", Path: "/" + request.Operation, Action: request.Operation}
	if request.Operation == "deposit" || request.Operation == "withdraw" {
		audit.AccountID = request.Account
		audit.Path = "/accounts/" + request.Account + "/" + request.Operation
	}
	username := strings.TrimSpace(request.Username)
	if utf8.RuneCountInString(username) <= 64 {
		audit.Username = username
	}
	needsAudit := request.Operation == "login" || request.Operation == "deposit" || request.Operation == "withdraw"
	saved := false
	defer func() {
		if recover() != nil {
			response = errorResponse(id, errors.New("handler panic"))
		}
		if encoded, err := json.Marshal(response); err != nil {
			response = errorResponse(id, err)
		} else if len(encoded) > ResponseLimit {
			response = errorResponse(id, service.Fail(500, "response_too_large", "Response exceeds 1 MiB limit"))
		}
		if needsAudit && !saved {
			audit.Result = response.Status
			audit.StatusCode = response.StatusCode
			audit.ErrorCode = response.Error
			ctx, cancel := context.WithTimeout(context.Background(), s.config.OperationTimeout)
			err := s.bank.DB.WithContext(ctx).Create(&audit).Error
			cancel()
			if err != nil {
				response = errorResponse(id, err)
				s.config.Logger.Printf("audit write failed request_id=%s", id)
			}
		}
		entry := map[string]any{"request_id": id, "peer_ip": ip, "operation": request.Operation, "result": response.Status, "status_code": response.StatusCode, "account": audit.AccountID, "latency_ms": float64(time.Since(start).Microseconds()) / 1000}
		if saved && audit.Balance != nil {
			entry["balance"] = *audit.Balance
		}
		encoded, _ := json.Marshal(entry)
		s.config.Logger.Print(string(encoded))
	}()
	if decodeErr != nil {
		return errorResponse(id, service.Fail(400, "malformed_request", "Invalid request envelope"))
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return errorResponse(id, service.Fail(429, "server_busy", "Maximum concurrent requests reached"))
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config.OperationTimeout)
	defer cancel()
	response = Response{Version: 1, RequestID: id, Status: "success", StatusCode: 200}
	public := request.Operation == "health" || request.Operation == "create_user" || request.Operation == "login"
	switch request.Operation {
	case "health", "create_user", "login", "logout", "me", "list_users", "get_user", "balance", "deposit", "withdraw":
	default:
		return errorResponse(id, service.Fail(400, "unsupported_action", "Unsupported operation"))
	}
	var userID uint
	var err error
	if request.Operation == "login" && !s.limiter.allow(ip) {
		return errorResponse(id, service.Fail(429, "server_busy", "too many login attempts"))
	}
	if !public {
		userID, err = s.sessions.Validate(ctx, request.Token)
		if err != nil {
			return errorResponse(id, err)
		}
		audit.UserID = &userID
	}
	switch request.Operation {
	case "health":
		response.Data = map[string]string{"status": "ok"}
	case "create_user":
		response.Data, err = s.bank.CreateUser(ctx, service.Credentials{Username: request.Username, Password: request.Password})
		response.StatusCode = 201
	case "login":
		var user models.User
		user, err = s.bank.Verify(ctx, service.Credentials{Username: request.Username, Password: request.Password})
		if err == nil {
			audit.UserID = &user.ID
			response.Data, err = s.sessions.Issue(ctx, user.ID, audit)
			saved = err == nil
		}
	case "logout":
		err = s.sessions.Logout(ctx, request.Token)
		response.Data = map[string]bool{"logged_out": err == nil}
	case "me":
		response.Data, err = s.bank.User(ctx, userID)
	case "list_users":
		response.Data, err = s.bank.Users(ctx)
	case "get_user":
		if request.ID == 0 {
			err = service.Fail(400, "malformed_request", "invalid user ID")
		} else {
			response.Data, err = s.bank.User(ctx, request.ID)
		}
	case "balance", "deposit", "withdraw":
		if request.Account == "" {
			err = service.Fail(400, "missing_field", "Account is required")
			break
		}
		var a *models.Account
		if request.Operation == "balance" {
			a, err = s.bank.Balance(ctx, userID, request.Account)
		} else {
			var amount int64
			if len(request.Amount) == 0 {
				err = service.Fail(400, "missing_field", "Amount is required")
			} else if string(request.Amount) == "null" || json.Unmarshal(request.Amount, &amount) != nil || amount <= 0 {
				err = service.Fail(400, "invalid_amount", "Amount must be a positive int64 integer")
			} else {
				audit.Amount = &amount
				a, err = s.bank.Change(ctx, userID, request.Account, amount, request.Operation == "withdraw", audit)
				saved = err == nil
			}
		}
		if err == nil {
			audit.Balance = &a.Balance
			response.Data = map[string]any{"status": "success", "account": a.ID, "balance": a.Balance}
		}
	}
	if err != nil {
		return errorResponse(id, err)
	}
	return response
}
