package service

import (
	"context"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignupValidationAndAtomicRollback(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	defer sql.Close()
	bank := New(db)
	ctx := context.Background()
	for _, input := range []Credentials{{" ", "password123"}, {strings.Repeat("a", 65), "password123"}, {"alice", "short"}, {"alice", strings.Repeat("x", 73)}} {
		if _, err := bank.CreateUser(ctx, input); err == nil {
			t.Fatal("invalid signup accepted")
		}
	}
	user, err := bank.CreateUser(ctx, Credentials{strings.Repeat("ب", 64), strings.Repeat("x", 72)})
	if err != nil {
		t.Fatal(err)
	}
	var a models.Account
	if err = db.First(&a, "id = ?", user.AccountID).Error; err != nil || a.Balance != 0 {
		t.Fatal(a, err)
	}
	if err = db.Exec("DROP TABLE accounts").Error; err != nil {
		t.Fatal(err)
	}
	if _, err = bank.CreateUser(ctx, Credentials{"rollback", "password123"}); err == nil {
		t.Fatal("signup succeeded without account")
	}
	var count int64
	db.Model(&models.User{}).Where("username = ?", "rollback").Count(&count)
	if count != 0 {
		t.Fatal("non-atomic signup")
	}
}
