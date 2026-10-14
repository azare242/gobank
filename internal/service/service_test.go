package service

import (
	"context"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"path/filepath"
	"testing"
)

func TestTransactions(t *testing.T) {
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
	audit := models.AuditLog{Method: "POST", Path: "/accounts/" + user.AccountID + "/deposit"}
	if _, err = bank.Change(ctx, user.ID, user.AccountID, 100, false, audit); err != nil {
		t.Fatal(err)
	}
	if _, err = bank.Change(ctx, user.ID, user.AccountID, 101, true, audit); err == nil {
		t.Fatal("overdraw accepted")
	}
	if _, err = bank.Balance(ctx, user.ID+1, user.AccountID); err == nil {
		t.Fatal("ownership bypass")
	}
	db.Exec("DROP TABLE audit_logs")
	if _, err = bank.Change(ctx, user.ID, user.AccountID, 10, false, audit); err == nil {
		t.Fatal("audit failure accepted")
	}
	balance, err := bank.Balance(ctx, user.ID, user.AccountID)
	if err != nil || balance.Balance != 100 {
		t.Fatal(balance, err)
	}
}
