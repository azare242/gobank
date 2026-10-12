package socketserver

import (
	"context"
	"encoding/json"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"io"
	"log"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistentBanking(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	defer sql.Close()
	c := DefaultConfig()
	c.Logger = log.New(io.Discard, "", 0)
	s, err := New(db, c)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- s.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		if err := <-exited; err != nil {
			t.Error(err)
		}
	}()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { conn.Close() }()
	call := func(request map[string]any, want int) Response {
		t.Helper()
		request["version"] = 1
		request["request_id"] = "test-id"
		encoded, _ := json.Marshal(request)
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := WriteFrame(conn, encoded, RequestLimit); err != nil {
			t.Fatal(err)
		}
		encoded, err := ReadFrame(conn, ResponseLimit)
		if err != nil {
			t.Fatal(err)
		}
		var response Response
		if err = json.Unmarshal(encoded, &response); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want || response.RequestID != "test-id" {
			t.Fatalf("%v: %+v", request["operation"], response)
		}
		return response
	}
	call(map[string]any{"operation": "health"}, 200)
	created := call(map[string]any{"operation": "create_user", "username": "alice", "password": "password123"}, 201)
	account := created.Data.(map[string]any)["account"].(string)
	call(map[string]any{"operation": "login", "username": "alice", "password": "wrongpass"}, 401)
	login := call(map[string]any{"operation": "login", "username": "alice", "password": "password123"}, 200)
	token := login.Data.(map[string]any)["session_token"].(string)
	for _, op := range []string{"me", "list_users", "get_user", "balance"} {
		call(map[string]any{"operation": op, "token": token, "id": 1, "account": account}, 200)
	}
	for _, amount := range []any{0, -1, 1.5, "100", nil} {
		call(map[string]any{"operation": "deposit", "token": token, "account": account, "amount": amount}, 400)
	}
	call(map[string]any{"operation": "deposit", "token": token, "account": account, "amount": 100}, 200)
	call(map[string]any{"operation": "withdraw", "token": token, "account": account, "amount": 101}, 409)
	call(map[string]any{"operation": "withdraw", "token": token, "account": account, "amount": 40}, 200)
	call(map[string]any{"operation": "balance", "token": token, "account": "A9999"}, 404)
	conn.Close()
	conn, err = net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	result := call(map[string]any{"operation": "balance", "token": token, "account": account}, 200)
	if result.Data.(map[string]any)["balance"].(float64) != 60 {
		t.Fatal(result)
	}
	call(map[string]any{"operation": "logout", "token": token}, 200)
	call(map[string]any{"operation": "me", "token": token}, 401)
	for i := 0; i < 8; i++ {
		call(map[string]any{"operation": "login", "username": "alice", "password": "wrongpass"}, 401)
	}
	call(map[string]any{"operation": "login", "username": "alice", "password": "password123"}, 429)
	var count int64
	db.Model(&models.AuditLog{}).Where("action = ? AND result = ?", "deposit", "success").Count(&count)
	if count != 1 {
		t.Fatal("duplicate deposit audits", count)
	}
	s.slots <- struct{}{}
	for len(s.slots) < cap(s.slots) {
		s.slots <- struct{}{}
	}
	response := s.process([]byte(`{"version":1,"operation":"deposit","account":"A1001","amount":5}`), "127.0.0.1")
	if response.StatusCode != 429 {
		t.Fatal(response)
	}
	for len(s.slots) > 0 {
		<-s.slots
	}
	response = s.process([]byte(`{"version":1,"operation":"health"}`), "127.0.0.1")
	if response.StatusCode != 200 {
		t.Fatal("slot leak", response)
	}
}
func TestLimiterBoundAndExpiration(t *testing.T) {
	l := loginLimiter{entries: map[string]attempt{}}
	for i := 0; i < 10; i++ {
		if !l.allow("ip") {
			t.Fatal(i)
		}
	}
	if l.allow("ip") {
		t.Fatal("limit bypass")
	}
	l.entries["ip"] = attempt{count: 10, expires: time.Now().Add(-time.Second)}
	if !l.allow("ip") {
		t.Fatal("expired entry retained")
	}
	for i := 0; i < 4096; i++ {
		l.entries[string(rune(i+100))] = attempt{expires: time.Now().Add(time.Minute)}
	}
	if l.allow("new") {
		t.Fatal("storage unbounded")
	}
}

func TestOversizedListingIsExplicitError(t *testing.T) {
	s, _, db := setupLifecycle(t, DefaultConfig())
	// Populate public profiles directly; password hashing is covered by service tests.
	if err := db.Exec(`WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<12000)
 INSERT INTO users(username,password,account_id) SELECT printf('%064d',n),'unused','A'||(n+1000) FROM ids`).Error; err != nil {
		t.Fatal(err)
	}
	login, err := s.sessions.Issue(context.Background(), 1, models.AuditLog{Action: "login", Method: "TCP", Path: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(Request{Version: 1, Operation: "list_users", Token: login.Token})
	response := s.process(data, "ip")
	if response.Error != "response_too_large" || response.Data != nil {
		t.Fatal(response)
	}
}

func TestInvalidEnvelopeRetainsAuditIntent(t *testing.T) {
	s, _, db := setupLifecycle(t, DefaultConfig())
	for _, payload := range []string{
		`{"version":1,"operation":"login","username":null,"password":"password123"}`,
		`{"version":1,"operation":"deposit","account":"A1001","token":null,"amount":5}`,
	} {
		if response := s.process([]byte(payload), "ip"); response.StatusCode != 400 {
			t.Fatal(response)
		}
	}
	var entries []models.AuditLog
	if err := db.Order("id").Find(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != "login" || entries[1].Action != "deposit" || entries[1].AccountID != "A1001" {
		t.Fatal(entries)
	}
}
