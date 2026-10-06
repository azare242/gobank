package api

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/database"
)

func TestLoginAndMiddleware(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	tokens, err := auth.New(auth.Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "test", Audience: "api"})
	if err != nil {
		t.Fatal(err)
	}
	app := New(db, tokens)
	request := func(method, path, body, header string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, resp.StatusCode, status, data)
		}
		if path == "/login" && status == 200 && resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("login response is cacheable")
		}
		return data
	}
	request("POST", "/users", `{"username":"alice","password":"example-password"}`, "", 201)
	request("POST", "/login", `{`, "", 400)
	wrong := request("POST", "/login", `{"username":"alice","password":"wrong-password"}`, "", 401)
	missing := request("POST", "/login", `{"username":"missing","password":"wrong-password"}`, "", 401)
	if string(wrong) != string(missing) {
		t.Fatal("credential errors reveal account existence")
	}
	data := request("POST", "/login", `{"username":" alice ","password":"example-password"}`, "", 200)
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.TokenType != "Bearer" || result.ExpiresIn != 900 {
		t.Fatalf("invalid response: %s", data)
	}
	for _, header := range []string{"", "Basic credentials", "Bearer invalid", "Bearer a b"} {
		request("GET", "/me", "", header, 401)
	}
	request("GET", "/users", "", "", 401)
	request("GET", "/users/1", "", "", 401)
	me := request("GET", "/me", "", "Bearer "+result.AccessToken, 200)
	if strings.Contains(string(me), "password") || !strings.Contains(string(me), "alice") {
		t.Fatalf("unexpected profile: %s", me)
	}
	if err := db.Exec("DELETE FROM users WHERE id = ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	request("GET", "/me", "", "Bearer "+result.AccessToken, 401)
	for i := 0; i < 6; i++ {
		request("POST", "/login", `{}`, "", 401)
	}
	request("POST", "/login", `{}`, "", 429)
}
