package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
)

func TestAuditLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	tokens, _ := auth.New(auth.Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "test", Audience: "api"})
	app := New(db, tokens)
	token, _ := tokens.Generate(1)
	request := func(path, body string, status int) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Request-ID", "audit-test")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatalf("%s: %d, want %d", path, resp.StatusCode, status)
		}
	}
	request("/users", `{"username":"alice","password":"example-password"}`, 201)
	request("/login", `{"username":"alice","password":"example-password"}`, 200)
	request("/login", `{"username":"alice","password":"wrong-password"}`, 401)
	request("/login", `{`, 400)
	request("/accounts/A1001/deposit", `{"amount":100}`, 200)
	request("/accounts/A1001/withdraw", `{"amount":30}`, 200)
	request("/accounts/A1001/withdraw", `{"amount":100}`, 409)
	request("/accounts/A1001/deposit", `{}`, 400)
	var entries []models.AuditLog
	if err := db.Order("id").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 7 {
		t.Fatalf("audit count: %d, want 7", len(entries))
	}
	for _, entry := range entries {
		if entry.RequestID != "audit-test" || entry.CreatedAt.IsZero() || entry.ClientIP == "" {
			t.Fatalf("missing metadata: %+v", entry)
		}
	}
	login := entries[0]
	if login.Action != "login" || login.UserID == nil || *login.UserID != 1 || login.Username != "alice" || login.Result != "success" {
		t.Fatalf("login: %+v", login)
	}
	if entries[1].Result != "error" || entries[1].StatusCode != 401 || entries[2].StatusCode != 400 {
		t.Fatal("login failures not audited")
	}
	for i, balance := range []int64{100, 70} {
		entry := entries[3+i]
		if entry.Balance == nil || *entry.Balance != balance || entry.Amount == nil || entry.AccountID != "A1001" || entry.UserID == nil || entry.Result != "success" {
			t.Fatalf("transaction: %+v", entry)
		}
	}
	if entries[5].ErrorCode != "insufficient_funds" || entries[5].Balance != nil || entries[6].ErrorCode != "missing_field" {
		t.Fatal("transaction failures not audited")
	}
	// A failed successful-transaction audit insert must roll back the balance update.
	if err := db.Exec(`CREATE TRIGGER reject_success_audit BEFORE INSERT ON audit_logs WHEN NEW.result = 'success' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`).Error; err != nil {
		t.Fatal(err)
	}
	request("/accounts/A1001/deposit", `{"amount":20}`, 500)
	var account models.Account
	if err := db.First(&account, "id = ?", "A1001").Error; err != nil {
		t.Fatal(err)
	}
	if account.Balance != 70 {
		t.Fatalf("audit failure did not roll back balance: %d", account.Balance)
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
	var count int64
	if err := reopened.Model(&models.AuditLog{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("persisted audit count: %d, want 8", count)
	}
}
