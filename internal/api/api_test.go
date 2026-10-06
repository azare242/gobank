package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"workshop/s2/internal/database"
	"workshop/s2/internal/models"
)

func TestUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	app := New(db)
	request := func(method, path, body string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d; body: %s", method, path, resp.StatusCode, want, data)
		}
		if strings.Contains(string(data), "password") && resp.StatusCode < 400 {
			t.Fatalf("response exposes password field: %s", data)
		}
		return data
	}

	request("GET", "/health", "", 200)
	if body := request("GET", "/users", "", 200); string(body) != "[]" {
		t.Fatalf("empty list: %s", body)
	}
	body := request("POST", "/users", `{"username":" alice ","password":"example-password"}`, 201)
	var user models.User
	if err := json.Unmarshal(body, &user); err != nil {
		t.Fatal(err)
	}
	if user.ID == 0 || user.Username != "alice" {
		t.Fatalf("unexpected user: %+v", user)
	}
	var stored models.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.Password), []byte("example-password")); err != nil {
		t.Fatalf("stored password is not a valid matching bcrypt hash: %v", err)
	}
	request("POST", "/users", `{"username":"alice","password":"another-password"}`, 409)
	got := request("GET", fmt.Sprintf("/users/%d", user.ID), "", 200)
	if string(got) != string(body) {
		t.Fatalf("retrieved user differs: %s vs %s", got, body)
	}
	var users []models.User
	if err := json.Unmarshal(request("GET", "/users", "", 200), &users); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].ID != user.ID {
		t.Fatalf("unexpected list: %+v", users)
	}
	for _, invalid := range []string{
		`{`,
		`{}`,
		`{"username":"   ","password":"example-password"}`,
		`{"username":"bob","password":"short"}`,
		fmt.Sprintf(`{"username":"bob","password":%q}`, strings.Repeat("x", 73)),
		fmt.Sprintf(`{"username":%q,"password":"example-password"}`, strings.Repeat("x", 65)),
	} {
		request("POST", "/users", invalid, 400)
	}
	request("GET", "/users/0", "", 400)
	request("GET", "/users/abc", "", 400)
	request("GET", "/users/999", "", 404)

	// Opening the same file again must preserve the existing users.
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSQL, err := reopened.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedSQL.Close() })
	if err := reopened.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("user did not persist: %v", err)
	}
	if stored.Username != "alice" {
		t.Fatalf("unexpected persisted username: %q", stored.Username)
	}
}
