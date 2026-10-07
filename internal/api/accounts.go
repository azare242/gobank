package api

import (
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/models"
	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
)

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                 { return e.message }
func fail(status int, code, message string) error { return &apiError{status, code, message} }

func writeError(c fiber.Ctx, status int, message string, err error) error {
	code := "internal_error"
	var specific *apiError
	if errors.As(err, &specific) {
		status, code, message = specific.status, specific.code, specific.message
	} else {
		switch status {
		case 400, 413:
			code = "malformed_request"
		case 401:
			code = "unauthorized"
		case 403:
			code = "forbidden"
		case 404, 405:
			code = "unsupported_action"
		case 409:
			code = "conflict"
		case 429:
			code = "server_busy"
		default:
			if status >= 500 {
				message = "Internal server error"
			}
		}
	}
	return c.Status(status).JSON(fiber.Map{"status": "error", "error": code, "message": message})
}

func registerAccounts(app *fiber.App, db *gorm.DB, tokens *auth.Service) {
	// Bound the number of account requests in flight; never queue beyond the limit.
	slots := make(chan struct{}, 100)
	accounts := app.Group("/accounts", func(c fiber.Ctx) error {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			return c.Next()
		default:
			return fail(429, "server_busy", "Maximum concurrent clients reached")
		}
	}, tokens.Middleware())
	lookup := func(c fiber.Ctx, tx *gorm.DB) (*models.Account, error) {
		userID, _ := auth.UserID(c)
		var account models.Account
		if err := tx.Where("id = ? AND user_id = ?", c.Params("id"), userID).First(&account).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fail(404, "unknown_account", "Account not found")
			}
			return nil, err
		}
		return &account, nil
	}
	respond := func(c fiber.Ctx, account *models.Account) error {
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"status": "success", "account": account.ID, "balance": account.Balance})
	}
	accounts.Get("/:id/balance", func(c fiber.Ctx) error {
		account, err := lookup(c, db)
		if err != nil {
			return err
		}
		return respond(c, account)
	})
	change := func(withdraw bool) fiber.Handler {
		return func(c fiber.Ctx) error {
			var body map[string]json.RawMessage
			if err := json.Unmarshal(c.Body(), &body); err != nil || body == nil {
				return fail(400, "malformed_request", "Invalid JSON body")
			}
			raw, ok := body["amount"]
			if !ok {
				return fail(400, "missing_field", "Amount is required")
			}
			var amount int64
			if string(raw) == "null" || json.Unmarshal(raw, &amount) != nil || amount <= 0 {
				return fail(400, "invalid_amount", "Amount must be a positive int64 integer")
			}
			var result *models.Account
			err := db.Transaction(func(tx *gorm.DB) error {
				account, err := lookup(c, tx)
				if err != nil {
					return err
				}
				query := tx.Model(&models.Account{}).Where("id = ?", account.ID)
				if withdraw {
					query = query.Where("balance >= ?", amount).UpdateColumn("balance", gorm.Expr("balance - ?", amount))
				} else {
					query = query.Where("balance <= ?", math.MaxInt64-amount).UpdateColumn("balance", gorm.Expr("balance + ?", amount))
				}
				if query.Error != nil {
					return query.Error
				}
				if query.RowsAffected == 0 {
					if withdraw {
						return fail(409, "insufficient_funds", "Account does not have enough balance")
					}
					return fail(400, "invalid_amount", "Deposit would exceed maximum balance")
				}
				result, err = lookup(c, tx)
				return err
			})
			if err != nil {
				return err
			}
			return respond(c, result)
		}
	}
	accounts.Post("/:id/deposit", change(false))
	accounts.Post("/:id/withdraw", change(true))
	accounts.All("/:id/:action", func(c fiber.Ctx) error {
		return fail(400, "unsupported_action", "Unsupported account action: "+strings.TrimSpace(c.Params("action")))
	})
}
