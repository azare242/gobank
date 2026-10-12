package repository_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/repository"
	"github.com/google/uuid"
)

// Run the same repository contract on SQLite and optionally a PostgreSQL test DB.
func TestGORMStore(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "repository.db")
			if driver == "postgres" {
				dsn = os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("set TEST_POSTGRES_DSN to run PostgreSQL integration tests")
				}
			}
			db, err := database.OpenConfig(database.Config{Driver: driver, DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			t.Cleanup(func() { _ = sqlDB.Close() })
			store := repository.NewGORM(db).WithContext(context.Background())
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := store.WithContext(ctx).Users().List(); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled context: %v", err)
			}
			rollback := errors.New("roll back test fixtures")
			user := models.User{Username: uuid.NewString(), Password: "test-hash"}
			err = store.Transaction(func(tx repository.Store) error {
				if err := tx.Users().Create(&user); err != nil {
					return err
				}
				user.AccountID = uuid.NewString()
				if err := tx.Accounts().Create(&models.Account{ID: user.AccountID, UserID: user.ID}); err != nil {
					return err
				}
				if err := tx.Users().SetAccount(&user); err != nil {
					return err
				}
				if _, err := tx.Users().ByID(0); !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("zero user ID: %v", err)
				}
				found, err := tx.Users().ByUsername(user.Username)
				if err != nil || found.Password != user.Password {
					t.Fatalf("login lookup: %v, %v", found, err)
				}
				profile, err := tx.Users().ByID(user.ID)
				if err != nil || profile.AccountID != user.AccountID || profile.Password != "" {
					t.Fatalf("profile lookup: %v, %v", profile, err)
				}
				if _, err := tx.Accounts().ByOwner(user.AccountID, user.ID+1); !errors.Is(err, repository.ErrNotFound) {
					t.Fatalf("ownership: %v", err)
				}
				duplicateErr := tx.Transaction(func(nested repository.Store) error {
					return nested.Users().Create(&models.User{Username: user.Username, Password: "other"})
				})
				if !errors.Is(duplicateErr, repository.ErrDuplicate) {
					t.Fatalf("duplicate: %v", duplicateErr)
				}
				if err := tx.Accounts().ChangeBalance(user.AccountID, user.ID, math.MaxInt64, false); err != nil {
					return err
				}
				if err := tx.Accounts().ChangeBalance(user.AccountID, user.ID, 1, false); !errors.Is(err, repository.ErrBalanceOverflow) {
					t.Fatalf("overflow: %v", err)
				}
				if err := tx.Accounts().ChangeBalance(user.AccountID, user.ID, math.MaxInt64, true); err != nil {
					return err
				}
				if err := tx.Accounts().ChangeBalance(user.AccountID, user.ID, 1, true); !errors.Is(err, repository.ErrInsufficientFunds) {
					t.Fatalf("overdraft: %v", err)
				}
				if err := tx.Accounts().ChangeBalance(user.AccountID, user.ID, 0, false); !errors.Is(err, repository.ErrInvalidAmount) {
					t.Fatalf("invalid amount: %v", err)
				}
				// Balance and audit operations share the transaction's connection.
				err = tx.Transaction(func(nested repository.Store) error {
					if err := nested.Accounts().ChangeBalance(user.AccountID, user.ID, 50, false); err != nil {
						return err
					}
					return nested.AuditLogs().Create(&models.AuditLog{Action: "deposit"})
				})
				// A later failed operation must roll back its balance change.
				if err != nil {
					return err
				}
				err = tx.Transaction(func(nested repository.Store) error {
					if err := nested.Accounts().ChangeBalance(user.AccountID, user.ID, 10, false); err != nil {
						return err
					}
					return rollback
				})
				if !errors.Is(err, rollback) {
					t.Fatalf("transaction: %v", err)
				}
				account, err := tx.Accounts().ByOwner(user.AccountID, user.ID)
				if err != nil || account.Balance != 50 {
					t.Fatalf("rollback balance: %v, %v", account, err)
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			if _, err := store.Users().ByUsername(user.Username); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("fixture rollback: %v", err)
			}
		})
	}
}
