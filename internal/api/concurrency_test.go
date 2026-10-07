package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
)

func TestConcurrentBalancesPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bank.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	tokens, _ := auth.New(auth.Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "test", Audience: "api"})
	app := New(db, tokens)
	token, _ := tokens.Generate(1)
	call := func(action string, amount int) (int, error) {
		req := httptest.NewRequest("POST", "/accounts/A1001/"+action, strings.NewReader(fmt.Sprintf(`{"amount":%d}`, amount)))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return resp.StatusCode, nil
	}
	// Use the actual signup path to create both records.
	req := httptest.NewRequest("POST", "/users", strings.NewReader(`{"username":"alice","password":"example-password"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("signup: %d", resp.StatusCode)
	}
	parallel := func(action string, amount, count int) map[int]int {
		var wg sync.WaitGroup
		statuses := make(chan int, count)
		for i := 0; i < count; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, err := call(action, amount)
				if err != nil {
					t.Error(err)
				}
				statuses <- status
			}()
		}
		wg.Wait()
		close(statuses)
		counts := map[int]int{}
		for status := range statuses {
			counts[status]++
		}
		return counts
	}
	if counts := parallel("deposit", 10, 20); counts[200] != 20 {
		t.Fatalf("deposits: %v", counts)
	}
	if counts := parallel("withdraw", 30, 20); counts[200] != 6 || counts[409] != 14 {
		t.Fatalf("withdrawals: %v", counts)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSQL, _ := reopened.DB()
	defer reopenedSQL.Close()
	var account models.Account
	if err := reopened.First(&account, "id = ?", "A1001").Error; err != nil {
		t.Fatal(err)
	}
	if account.Balance != 20 {
		t.Fatalf("persisted balance: %d, want 20", account.Balance)
	}
}

func TestConcurrencyLimitAndRequestID(t *testing.T) {
	app := fiber.New(fiber.Config{ErrorHandler: func(c fiber.Ctx, err error) error { return writeError(c, 500, "Internal server error", err) }})
	app.Use(requestID(), concurrentRequests(1))
	entered, release := make(chan struct{}), make(chan struct{})
	app.Get("/hold", func(c fiber.Ctx) error { close(entered); <-release; return c.SendStatus(200) })
	app.Get("/health", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"status": "ok"}) })
	done := make(chan error, 1)
	go func() {
		resp, err := app.Test(httptest.NewRequest("GET", "/hold", nil))
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not enter")
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("X-Request-ID", "test-request")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if resp.StatusCode != 429 || body["error"] != "server_busy" || resp.Header.Get("X-Request-ID") != "test-request" {
		t.Fatalf("rejection: %d %v", resp.StatusCode, body)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	resp, err = app.Test(httptest.NewRequest("GET", "/health", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("slot not released")
	}
	if _, err := uuid.Parse(resp.Header.Get("X-Request-ID")); err != nil {
		t.Fatalf("missing generated UUID: %v", err)
	}
}
