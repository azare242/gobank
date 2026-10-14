package api

import (
	"fmt"
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
		level := "INFO"
		if status >= 500 {
			level = "ERROR"
		} else if status >= 400 {
			level = "WARN"
		}
		line := fmt.Sprintf("%s | %-5s | %d | %-7s %q | %8.3f ms | result=%s request_id=%q client_ip=%q action=%q",
			start.UTC().Format("2006-01-02 15:04:05.000Z"), level, status, c.Method(), c.Path(),
			float64(time.Since(start).Microseconds())/1000, result, c.Locals("request_id"), c.IP(), action)
		if account != "" {
			line += fmt.Sprintf(" account=%q", account)
		}
		if balance := c.Locals("balance"); balance != nil {
			line += fmt.Sprintf(" balance=%v", balance)
		}
		requestLogger.Print(line)
		return err
	}
}
