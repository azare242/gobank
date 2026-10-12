package socketserver

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/service"
	"gorm.io/gorm"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func seedSession(t *testing.T, s *Server) (string, string) {
	t.Helper()
	u, err := s.bank.CreateUser(context.Background(), service.Credentials{Username: "alice", Password: "password123"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.sessions.Issue(context.Background(), u.ID, models.AuditLog{Action: "login", Method: "TCP", Path: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	return login.Token, u.AccountID
}
func exchange(t *testing.T, c net.Conn, r Request) Response {
	t.Helper()
	r.Version = 1
	data, _ := json.Marshal(r)
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := WriteFrame(c, data, RequestLimit); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFrame(c, ResponseLimit)
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if err = json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
func TestConcurrentMutationsAndReusedSession(t *testing.T) {
	s, l, db := setupLifecycle(t, DefaultConfig())
	token, account := seedSession(t, s)
	startLifecycle(t, s, l)
	var wg sync.WaitGroup
	results := make(chan int, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", l.Addr().String())
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			r := exchange(t, conn, Request{Operation: "deposit", Token: token, Account: account, Amount: json.RawMessage(`10`)})
			results <- r.StatusCode
		}()
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 {
			t.Fatal(code)
		}
	}
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	balance := exchange(t, conn, Request{Operation: "balance", Token: token, Account: account})
	if balance.Data.(map[string]any)["balance"].(float64) != 240 {
		t.Fatal(balance)
	}
	results = make(chan int, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", l.Addr().String())
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			results <- exchange(t, c, Request{Operation: "withdraw", Token: token, Account: account, Amount: json.RawMessage(`20`)}).StatusCode
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for code := range results {
		if code == 200 {
			success++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if success != 12 {
		t.Fatal("conditional withdrawals failed", success)
	}
	db.Model(&models.Account{}).Where("id = ?", account).Update("balance", int64(math.MaxInt64))
	if r := exchange(t, conn, Request{Operation: "deposit", Token: token, Account: account, Amount: json.RawMessage(`1`)}); r.Error != "invalid_amount" {
		t.Fatal(r)
	}
	db.Model(&models.Session{}).Where("user_id = ?", 1).Update("expires_at", time.Now().UTC().Add(-time.Second))
	if r := exchange(t, conn, Request{Operation: "me", Token: token}); r.Error != "unauthorized" {
		t.Fatal("identity cached", r)
	}
}

type failWriteConn struct {
	net.Conn
	writing chan struct{}
}

func (c *failWriteConn) Write([]byte) (int, error) {
	select {
	case c.writing <- struct{}{}:
	default:
	}
	return 0, net.ErrClosed
}
func TestDisconnectBeforeFrameAndAfterCommit(t *testing.T) {
	s, _, db := setupLifecycle(t, DefaultConfig())
	token, account := seedSession(t, s)
	for _, complete := range []bool{false, true} {
		server, client := net.Pipe()
		writing := make(chan struct{}, 1)
		wrapped := &failWriteConn{server, writing}
		state := &connection{}
		s.mu.Lock()
		s.conns[wrapped] = state
		s.wg.Add(1)
		s.mu.Unlock()
		done := make(chan struct{})
		go func() { s.handle(wrapped, state); close(done) }()
		if complete {
			data, _ := json.Marshal(Request{Version: 1, Operation: "deposit", Token: token, Account: account, Amount: json.RawMessage(`25`)})
			WriteFrame(client, data, RequestLimit)
		} else {
			client.Write([]byte{0, 0, 0, 20, '{'})
		}
		client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("handler leaked")
		}
		var a models.Account
		db.First(&a, "id = ?", account)
		want := int64(0)
		if complete {
			want = 25
		}
		if a.Balance != want {
			t.Fatal(a)
		}
	}
	var count int64
	db.Model(&models.AuditLog{}).Where("action = ?", "deposit").Count(&count)
	if count != 1 {
		t.Fatal("lost or duplicate committed audit", count)
	}
}
func TestDrainAdmittedOperationAndAuditRedaction(t *testing.T) {
	s, l, db := setupLifecycle(t, DefaultConfig())
	token, account := seedSession(t, s)
	var logs bytes.Buffer
	s.config.Logger.SetOutput(&logs)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	db.Callback().Update().Before("gorm:update").Register("pause", func(*gorm.DB) { once.Do(func() { close(entered); <-release }) })
	startLifecycle(t, s, l)
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	result := make(chan Response, 1)
	go func() {
		result <- exchange(t, conn, Request{Operation: "deposit", Token: token, Account: account, Amount: json.RawMessage(`50`)})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not admitted")
	}
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopped <- s.Shutdown(ctx)
	}()
	select {
	case err := <-stopped:
		t.Fatal("did not drain", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if r := <-result; r.StatusCode != 200 {
		t.Fatal(r)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "password123") {
		t.Fatal("secret logged")
	}
	var entries []models.AuditLog
	db.Find(&entries)
	encoded, _ := json.Marshal(entries)
	if bytes.Contains(encoded, []byte(token)) || bytes.Contains(encoded, []byte("password123")) {
		t.Fatal("secret audited")
	}
	s.mu.Lock()
	count := len(s.conns)
	s.mu.Unlock()
	if count != 0 || len(s.slots) != 0 {
		t.Fatal("resources leaked", count, len(s.slots))
	}
}

func TestOperationDeadlineAndFailedAudit(t *testing.T) {
	c := DefaultConfig()
	c.OperationTimeout = 20 * time.Millisecond
	s, _, db := setupLifecycle(t, c)
	db.Callback().Query().Before("gorm:query").Register("deadline", func(tx *gorm.DB) { <-tx.Statement.Context.Done() })
	r := s.process([]byte(`{"version":1,"operation":"login","username":"alice","password":"password123"}`), "ip")
	db.Callback().Query().Remove("deadline")
	if r.StatusCode != 500 || len(s.slots) != 0 {
		t.Fatal(r)
	}
	var audit models.AuditLog
	if err := db.First(&audit).Error; err != nil || audit.Result != "error" {
		t.Fatal(audit, err)
	}
}

func TestMalformedJSONKeepsFrameBoundaries(t *testing.T) {
	s, _, _ := setupLifecycle(t, DefaultConfig())
	server, client := net.Pipe()
	state := &connection{}
	s.mu.Lock()
	s.conns[server] = state
	s.wg.Add(1)
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.handle(server, state); close(done) }()
	client.SetDeadline(time.Now().Add(time.Second))
	if err := WriteFrame(client, []byte(`{`), RequestLimit); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFrame(client, ResponseLimit)
	if err != nil {
		t.Fatal(err)
	}
	var r Response
	if err = json.Unmarshal(data, &r); err != nil || r.Error != "malformed_request" {
		t.Fatal(r, err)
	}
	if r = exchange(t, client, Request{Operation: "health"}); r.StatusCode != 200 {
		t.Fatal("lost frame boundary", r)
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler leaked")
	}
}
