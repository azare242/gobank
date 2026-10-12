package api

import (
	"errors"
	"strings"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/service"
	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
)

func newAudit(c fiber.Ctx, action, account string) models.AuditLog {
	id, _ := c.Locals("request_id").(string)
	username, _ := c.Locals("audit_username").(string)
	entry := models.AuditLog{CreatedAt: time.Now().UTC(), RequestID: id, ClientIP: c.IP(), Username: username,
		AccountID: account, Action: action, Method: c.Method(), Path: c.Path()}
	if userID, ok := auth.UserID(c); ok {
		entry.UserID = &userID
	}
	if userID, ok := c.Locals("audit_user_id").(uint); ok {
		entry.UserID = &userID
	}
	if amount, ok := c.Locals("audit_amount").(int64); ok {
		entry.Amount = &amount
	}
	return entry
}

func auditRequests(db *gorm.DB) fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		action, account := "", ""
		if c.Method() == "POST" && c.Path() == "/login" {
			action = "login"
		}
		parts := strings.Split(strings.Trim(c.Path(), "/"), "/")
		if c.Method() == "POST" && len(parts) == 3 && parts[0] == "accounts" && (parts[2] == "deposit" || parts[2] == "withdraw") {
			action, account = parts[2], parts[1]
		}
		if action == "" {
			return c.Next()
		}
		// Recover here so panics are audited before the outer recovery handles them.
		defer func() {
			panicValue := recover()
			if panicValue != nil {
				err = errors.New("handler panic")
			}
			if c.Locals("audit_saved") != true {
				entry := newAudit(c, action, account)
				entry.StatusCode, entry.Result = c.Response().StatusCode(), "success"
				if err != nil {
					entry.StatusCode, entry.ErrorCode = 500, "internal_error"
					var specific *apiError
					var httpErr *fiber.Error
					var appErr *service.Error
					if errors.As(err, &specific) {
						entry.StatusCode, entry.ErrorCode = specific.status, specific.code
					} else if errors.As(err, &appErr) {
						entry.StatusCode, entry.ErrorCode = appErr.Status, appErr.Code
					} else if errors.As(err, &httpErr) {
						entry.StatusCode = httpErr.Code
						switch httpErr.Code {
						case 400:
							entry.ErrorCode = "malformed_request"
						case 401:
							entry.ErrorCode = "unauthorized"
						case 429:
							entry.ErrorCode = "server_busy"
						}
					}
				}
				if entry.StatusCode >= 400 {
					entry.Result = "error"
				}
				if saveErr := db.Create(&entry).Error; saveErr != nil {
					err = saveErr
				}
			}
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		return c.Next()
	}
}
