package socketcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/azare242/gobank/internal/socketclient"
	"github.com/azare242/gobank/internal/socketserver"
	"strings"
	"testing"
)

type fake struct {
	requests     []socketserver.Request
	failure      string
	unauthorized bool
	closed       bool
}

func (f *fake) Close() error { f.closed = true; return nil }
func (f *fake) Call(_ context.Context, r socketserver.Request) (socketclient.Response, error) {
	f.requests = append(f.requests, r)
	response := socketclient.Response{Status: "success", StatusCode: 200}
	var data any
	if r.Operation == f.failure {
		return response, errors.New("offline")
	}
	if f.unauthorized && r.Operation == "balance" {
		return socketclient.Response{Status: "error", StatusCode: 401, Error: "unauthorized", Message: "Unauthorized"}, nil
	}
	switch r.Operation {
	case "login":
		data = map[string]any{"session_token": strings.Repeat("x", 43), "expires_in": 900}
	case "create_user", "me":
		data = map[string]any{"id": 1, "username": "alice", "account": "A1001"}
	case "balance", "deposit", "withdraw":
		data = map[string]any{"account": "A1001", "balance": 60}
	default:
		data = map[string]string{"status": "ok"}
	}
	response.Data, _ = json.Marshal(data)
	return response, nil
}
func runMenu(t *testing.T, input string, f *fake) string {
	t.Helper()
	var out bytes.Buffer
	m := Menu{In: strings.NewReader(input), Out: &out, Client: f}
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.closed {
		t.Fatal("client leaked")
	}
	text := out.String()
	if strings.Contains(text, "password123") || strings.Contains(text, strings.Repeat("x", 43)) {
		t.Fatal("secret printed")
	}
	return text
}
func TestMenuTransitionsAndValidation(t *testing.T) {
	f := &fake{}
	text := runMenu(t, "bad\n1\n\nalice\nshort\npassword123\n2\nalice\npassword123\n1\n2\n3\n0\n1.5\n9223372036854775808\n50\nmaybe\nn\n3\n100\ny\n4\n40\ny\n5\n3\n4\n", f)
	if !strings.Contains(text, "Registered alice") || !strings.Contains(text, "Logged out") || !strings.Contains(text, "Cancelled") || !strings.Contains(text, "Server is healthy") {
		t.Fatal(text)
	}
	counts := map[string]int{}
	for _, r := range f.requests {
		counts[r.Operation]++
		if r.Operation == "me" || r.Operation == "balance" || r.Operation == "deposit" || r.Operation == "withdraw" || r.Operation == "logout" {
			if r.Token == "" {
				t.Fatal("missing session")
			}
		}
	}
	if counts["deposit"] != 1 || counts["withdraw"] != 1 || counts["logout"] != 1 {
		t.Fatal(counts)
	}
}
func TestUnauthorizedEOFAndLogoutFailure(t *testing.T) {
	for _, f := range []*fake{{unauthorized: true}, {failure: "logout"}, {}} {
		text := runMenu(t, "2\nalice\npassword123\n2\n", f)
		if f.unauthorized && !strings.Contains(text, "Please log in again") {
			t.Fatal(text)
		}
		if f.failure != "" && !strings.Contains(text, "remains until expiration") {
			t.Fatal(text)
		}
	}
	runMenu(t, "", &fake{})
}
func TestAmbiguousMutationNeverReplayed(t *testing.T) {
	f := &fake{failure: "deposit"}
	text := runMenu(t, "2\nalice\npassword123\n3\n100\ny\ny\n6\n", f)
	if !strings.Contains(text, "may have committed") {
		t.Fatal(text)
	}
	count := 0
	balance := 0
	for _, r := range f.requests {
		if r.Operation == "deposit" {
			count++
		}
		if r.Operation == "balance" {
			balance++
		}
	}
	if count != 1 || balance != 1 {
		t.Fatal(count, balance)
	}
}

func TestInjectedHiddenPasswordIsCleared(t *testing.T) {
	password := []byte("password123")
	f := &fake{}
	var out bytes.Buffer
	menu := Menu{In: strings.NewReader("2\nalice\n6\n"), Out: &out, Client: f, ReadPassword: func() ([]byte, error) { return password, nil }}
	if err := menu.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, b := range password {
		if b != 0 {
			t.Fatal("password byte buffer retained")
		}
	}
	if strings.Contains(out.String(), "password123") {
		t.Fatal("password displayed")
	}
}

func TestCancelledContextClosesAndAttemptsLogout(t *testing.T) {
	f := &fake{}
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	menu := Menu{In: strings.NewReader(""), Out: &out, Client: f, token: strings.Repeat("x", 43)}
	if err := menu.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.closed || len(f.requests) != 1 || f.requests[0].Operation != "logout" {
		t.Fatal("interrupt cleanup missing")
	}
}
