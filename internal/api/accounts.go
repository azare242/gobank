package api

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/repository"
	"github.com/gofiber/fiber/v3"
)

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string                 { return e.message }
func fail(status int, code, message string) error { return &apiError{status, code, message} }

func writeError(c fiber.Ctx, status int, message string, err error) error {
	code := "internal_error"
	var appErr *service.Error
	if errors.As(err, &appErr) {
		return c.Status(appErr.Status).JSON(fiber.Map{"status": "error", "error": appErr.Code, "message": appErr.Message})
	}
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

func registerAccounts(app *fiber.App, store repository.Store, tokens *auth.Service) {
	accounts := app.Group("/accounts", tokens.Middleware())
	lookup := func(c fiber.Ctx, tx repository.Store) (*models.Account, error) {
		userID, _ := auth.UserID(c)
		account, err := tx.Accounts().ByOwner(c.Params("id"), userID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, fail(404, "unknown_account", "Account not found")
			}
			return nil, err
		}
		return account, nil
	}
	respond := func(c fiber.Ctx, account *models.Account) error {
		c.Locals("balance", account.Balance)
		c.Set("Cache-Control", "no-store")
		return c.JSON(fiber.Map{"status": "success", "account": account.ID, "balance": account.Balance})
	}
	accounts.Get("/:id/balance", func(c fiber.Ctx) error {
		account, err := lookup(c, store.WithContext(c.Context()))
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
			c.Locals("audit_amount", amount)
			var result *models.Account
			err := store.WithContext(c.Context()).Transaction(func(tx repository.Store) error {
				account, err := lookup(c, tx)
				if err != nil {
					return err
				}
				userID, _ := auth.UserID(c)
				if err := tx.Accounts().ChangeBalance(account.ID, userID, amount, withdraw); err != nil {
					switch {
					case errors.Is(err, repository.ErrInsufficientFunds):
						return fail(409, "insufficient_funds", "Account does not have enough balance")
					case errors.Is(err, repository.ErrBalanceOverflow):
						return fail(400, "invalid_amount", "Deposit would exceed maximum balance")
					default:
						return err
					}
				}
				result, err = lookup(c, tx)
				if err != nil {
					return err
				}
				entry := newAudit(c, c.Params("action"), account.ID)
				// Matched routes have a literal action rather than an :action parameter.
				if withdraw {
					entry.Action = "withdraw"
				} else {
					entry.Action = "deposit"
				}
				entry.Result, entry.StatusCode = "success", 200
				entry.Balance = &result.Balance
				return tx.AuditLogs().Create(&entry)
			})
			if err != nil {
				return err
			}
			c.Locals("audit_saved", true)
			return respond(c, result)
		}
	}
	accounts.Post("/:id/deposit", change(false))
	accounts.Post("/:id/withdraw", change(true))
	accounts.All("/:id/:action", func(c fiber.Ctx) error {
		return fail(400, "unsupported_action", "Unsupported account action: "+strings.TrimSpace(c.Params("action")))
	})
}
