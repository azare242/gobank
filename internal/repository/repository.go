// Package repository defines persistence operations independently of GORM.
package repository

import (
	"context"
	"errors"

	"github.com/azare242/gobank/internal/models"
)

var (
	ErrNotFound          = errors.New("record not found")
	ErrDuplicate         = errors.New("duplicate record")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrBalanceOverflow   = errors.New("balance overflow")
	ErrInvalidAmount     = errors.New("amount must be positive")
)

type Users interface {
	Create(*models.User) error
	SetAccount(*models.User) error
	ByID(uint) (*models.User, error)
	ByUsername(string) (*models.User, error)
	List() ([]models.User, error)
}

type Accounts interface {
	Create(*models.Account) error
	ByOwner(string, uint) (*models.Account, error)
	ChangeBalance(string, uint, int64, bool) error
}

type AuditLogs interface {
	Create(*models.AuditLog) error
}

// Store binds repositories to one connection or transaction. Use the callback's
// Store for every operation that must commit or roll back together.
type Store interface {
	Users() Users
	Accounts() Accounts
	AuditLogs() AuditLogs
	WithContext(context.Context) Store
	Transaction(func(Store) error) error
}
