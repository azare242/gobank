package service

import (
	"context"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionPersistenceRevocationAndDeletedUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	db, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bank := New(db)
	user, err := bank.CreateUser(ctx, Credentials{"alice", "password123"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := Sessions{db, time.Minute}
	first, err := sessions.Issue(ctx, user.ID, models.AuditLog{Action: "login", Method: "TCP", Path: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	if err = sql.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sql, _ = db.DB()
	defer sql.Close()
	sessions.DB = db
	if id, err := sessions.Validate(ctx, first.Token); err != nil || id != user.ID {
		t.Fatal("session did not survive restart", id, err)
	}
	second, err := sessions.Issue(ctx, user.ID, models.AuditLog{Action: "login", Method: "TCP", Path: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token {
		t.Fatal("tokens reused")
	}
	if err = sessions.RevokeAll(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{first.Token, second.Token, "", "not-a-token"} {
		if _, err = sessions.Validate(ctx, token); err == nil {
			t.Fatal("invalid token authenticated")
		}
	}
	third, err := sessions.Issue(ctx, user.ID, models.AuditLog{Action: "login", Method: "TCP", Path: "/login"})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Delete(&models.User{}, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err = sessions.Validate(ctx, third.Token); err == nil {
		t.Fatal("deleted user authenticated")
	}
}
