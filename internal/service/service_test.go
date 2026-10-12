package service

import (
	"context"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"path/filepath"
	"testing"
	"time"
)

func TestTransactionsAndSessions(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	defer sql.Close()
	ctx := context.Background()
	bank := New(db)
	user, err := bank.CreateUser(ctx, Credentials{" alice ", "password123"})
	if err != nil || user.Username != "alice" {
		t.Fatal(user, err)
	}
	if _, err = bank.CreateUser(ctx, Credentials{"alice", "password123"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err = bank.Verify(ctx, Credentials{"absent", "password123"}); err == nil {
		t.Fatal("unknown login accepted")
	}
	if _, err = bank.Verify(ctx, Credentials{"alice", "password123"}); err != nil {
		t.Fatal(err)
	}
	sessions := Sessions{db, time.Minute}
	audit := models.AuditLog{Action: "login", Method: "TCP", Path: "/login"}
	login, err := sessions.Issue(ctx, user.ID, audit)
	if err != nil {
		t.Fatal(err)
	}
	var stored models.Session
	db.First(&stored)
	if stored.TokenHash == login.Token || len(stored.TokenHash) != 64 {
		t.Fatal("raw token stored")
	}
	if id, err := sessions.Validate(ctx, login.Token); err != nil || id != user.ID {
		t.Fatal(id, err)
	}
	if _, err = bank.Change(ctx, user.ID, user.AccountID, 100, false, audit); err != nil {
		t.Fatal(err)
	}
	if _, err = bank.Change(ctx, user.ID, user.AccountID, 101, true, audit); err == nil {
		t.Fatal("overdraw accepted")
	}
	if _, err = bank.Balance(ctx, user.ID+1, user.AccountID); err == nil {
		t.Fatal("ownership bypass")
	}
	if err = sessions.Logout(ctx, login.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Validate(ctx, login.Token); err == nil {
		t.Fatal("revocation failed")
	}
	login, err = sessions.Issue(ctx, user.ID, audit)
	if err != nil {
		t.Fatal(err)
	}
	db.Model(&models.Session{}).Where("user_id = ?", user.ID).Update("expires_at", time.Now().UTC().Add(-time.Second))
	if _, err = sessions.Validate(ctx, login.Token); err == nil {
		t.Fatal("expiration failed")
	}
	if err = sessions.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	db.Exec("DROP TABLE audit_logs")
	if _, err = bank.Change(ctx, user.ID, user.AccountID, 10, false, audit); err == nil {
		t.Fatal("audit failure accepted")
	}
	balance, err := bank.Balance(ctx, user.ID, user.AccountID)
	if err != nil || balance.Balance != 100 {
		t.Fatal(balance, err)
	}
	if _, err = sessions.Issue(ctx, user.ID, audit); err == nil {
		t.Fatal("login audit failure accepted")
	}
	var count int64
	db.Model(&models.Session{}).Count(&count)
	if count != 0 {
		t.Fatal("failed login left session")
	}
}
