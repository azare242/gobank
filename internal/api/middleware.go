package api

import (
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

var requestLogger = log.New(os.Stdout, "", 0)

func requestID() fiber.Handler {
	return func(c fiber.Ctx) error {
		id := c.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		c.Locals("request_id", id)
		c.Set("X-Request-ID", id)
		return c.Next()
	}
}

func concurrentRequests(max int) fiber.Handler {
	slots := make(chan struct{}, max)
	return func(c fiber.Ctx) error {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			return c.Next()
		default:
			return fail(429, "server_busy", "Maximum concurrent requests reached")
		}
	}
}

func requestLog(handleError fiber.ErrorHandler) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		if err != nil {
			err = handleError(c, err)
		}
		status := c.Response().StatusCode()
		result := "success"
		if status >= 400 {
			result = "error"
		}
		parts := strings.Split(strings.Trim(c.Path(), "/"), "/")
		action, account := c.Method(), ""
		if len(parts) == 3 && parts[0] == "accounts" {
			account, action = parts[1], parts[2]
		}
		entry := map[string]any{
			"timestamp":  start.UTC().Format(time.RFC3339Nano),
			"request_id": c.Locals("request_id"), "client_ip": c.IP(),
			"method": c.Method(), "path": c.Path(), "action": action,
			"account": account, "result": result, "status_code": status,
			"latency_ms": float64(time.Since(start).Microseconds()) / 1000,
		}
		if balance := c.Locals("balance"); balance != nil {
			entry["balance"] = balance
		}
		data, _ := json.Marshal(entry)
		requestLogger.Print(string(data))
		return err
	}
}
