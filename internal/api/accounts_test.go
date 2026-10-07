package api

import (
	"encoding/json"
	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/database"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccounts(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "bank.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	tokens, _ := auth.New(auth.Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "test", Audience: "api"})
	app := New(db, tokens)
	token, _ := tokens.Generate(1)
	request := func(method, path, body string, status int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var data map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s: status %d, expected %d: %v", path, resp.StatusCode, status, data)
		}
		return data
	}
	signup := request("POST", "/users", `{"username":"alice","password":"example-password"}`, 201)
	if signup["account"] != "A1001" {
		t.Fatalf("missing signup account: %v", signup)
	}
	for _, step := range []struct {
		method, path, body string
		status             int
		balance            float64
		code               string
	}{
		{"GET", "balance", "", 200, 0, ""},
		{"POST", "deposit", `{"amount":1000}`, 200, 1000, ""},
		{"POST", "withdraw", `{"amount":200}`, 200, 800, ""},
		{"POST", "withdraw", `{"amount":801}`, 409, 0, "insufficient_funds"},
		{"POST", "deposit", `{}`, 400, 0, "missing_field"},
		{"POST", "deposit", `{`, 400, 0, "malformed_request"},
		{"POST", "deposit", `{"amount":0}`, 400, 0, "invalid_amount"},
		{"POST", "deposit", `{"amount":1.5}`, 400, 0, "invalid_amount"},
		{"POST", "deposit", `{"amount":null}`, 400, 0, "invalid_amount"},
		{"POST", "deposit", `{"amount":9223372036854775807}`, 400, 0, "invalid_amount"},
		{"POST", "transfer", `{}`, 400, 0, "unsupported_action"},
		{"GET", "balance", "", 200, 800, ""},
	} {
		data := request(step.method, "/accounts/A1001/"+step.path, step.body, step.status)
		if step.code != "" {
			if data["status"] != "error" || data["error"] != step.code || data["message"] == nil {
				t.Fatalf("invalid error: %v", data)
			}
		} else if data["balance"] != step.balance || data["status"] != "success" {
			t.Fatalf("invalid balance: %v", data)
		}
	}
	request("GET", "/accounts/missing/balance", "", 404)
	token, _ = tokens.Generate(2)
	request("GET", "/accounts/A1001/balance", "", 404)
}
