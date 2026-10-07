package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
)

func TestStructuredRequestLogs(t *testing.T) {
	var output bytes.Buffer
	requestLogger.SetOutput(&output)
	defer requestLogger.SetOutput(os.Stdout)
	handle := func(c fiber.Ctx, err error) error { return writeError(c, 500, "Internal server error", err) }
	app := fiber.New(fiber.Config{ErrorHandler: handle})
	app.Use(requestID(), requestLog(handle), recover.New())
	app.Get("/accounts/:id/balance", func(c fiber.Ctx) error { c.Locals("balance", int64(800)); return c.JSON(fiber.Map{"balance": 800}) })
	app.Get("/error", func(c fiber.Ctx) error {
		return fail(409, "insufficient_funds", "Account does not have enough balance")
	})
	app.Get("/panic", func(c fiber.Ctx) error { panic("test panic") })
	for _, step := range []struct {
		path   string
		status int
	}{{"/accounts/A1001/balance", 200}, {"/error", 409}, {"/panic", 500}} {
		output.Reset()
		req := httptest.NewRequest("GET", step.path, nil)
		req.Header.Set("X-Request-ID", "log-test")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != step.status {
			t.Fatalf("status %d, want %d", resp.StatusCode, step.status)
		}
		var entry map[string]any
		if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
			t.Fatalf("invalid JSON log: %s: %v", output.String(), err)
		}
		if entry["request_id"] != "log-test" || entry["status_code"] != float64(step.status) || entry["path"] != step.path {
			t.Fatalf("incorrect log: %v", entry)
		}
		for _, field := range []string{"timestamp", "client_ip", "action", "account", "result", "latency_ms"} {
			if _, ok := entry[field]; !ok {
				t.Fatalf("missing %s", field)
			}
		}
		if step.status == 200 {
			if entry["account"] != "A1001" || entry["action"] != "balance" || entry["balance"] != float64(800) {
				t.Fatalf("incorrect account log: %v", entry)
			}
		} else if entry["result"] != "error" {
			t.Fatalf("incorrect error log: %v", entry)
		}
	}
}
