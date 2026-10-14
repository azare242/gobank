package api

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
)

func TestReadableRequestLogs(t *testing.T) {
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
		line := output.String()
		level := "INFO"
		if step.status >= 500 {
			level = "ERROR"
		} else if step.status >= 400 {
			level = "WARN"
		}
		for _, field := range []string{fmt.Sprintf("| %-5s | %d | GET", level, step.status), fmt.Sprintf("%q", step.path), `request_id="log-test"`, "client_ip=", "action=", " ms |"} {
			if !strings.Contains(line, field) {
				t.Fatalf("missing %q in log: %s", field, line)
			}
		}
		if strings.Count(line, "\n") != 1 {
			t.Fatalf("expected one log line: %q", line)
		}
		if step.status == 200 {
			if !strings.Contains(line, `account="A1001" balance=800`) || !strings.Contains(line, `action="balance"`) || !strings.Contains(line, "result=success") {
				t.Fatalf("incorrect account log: %s", line)
			}
		} else if !strings.Contains(line, "result=error") || strings.Contains(line, "balance=") {
			t.Fatalf("incorrect error log: %s", line)
		}
	}
}
