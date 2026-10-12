// Package service contains banking rules independent of transports and authentication mechanisms.
package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/repository"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type Error struct {
	Status        int
	Code, Message string
}

func (e *Error) Error() string                    { return e.Message }
func Fail(status int, code, message string) error { return &Error{status, code, message} }

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type Service struct {
	Store repository.Store
	dummy []byte
}

func New(db *gorm.DB) *Service {
	return NewWithRepository(repository.NewGORM(db))
}

func NewWithRepository(store repository.Store) *Service {
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy-login-comparison"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return &Service{Store: store, dummy: hash}
}
func (s *Service) CreateUser(ctx context.Context, input Credentials) (models.User, error) {
	input.Username = strings.TrimSpace(input.Username)
	var user models.User
	if input.Username == "" || input.Password == "" {
		return user, Fail(400, "missing_field", "Username and password are required")
	}
	if n := utf8.RuneCountInString(input.Username); n < 1 || n > 64 {
		return user, Fail(400, "malformed_request", "username must be 1 to 64 characters")
	}
	if n := len(input.Password); n < 8 || n > 72 {
		return user, Fail(400, "malformed_request", "password must be 8 to 72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return user, err
	}
	user = models.User{Username: input.Username, Password: string(hash)}
	err = s.Store.WithContext(ctx).Transaction(func(tx repository.Store) error {
		if err := tx.Users().Create(&user); err != nil {
			return err
		}
		user.AccountID = "A" + strconv.FormatUint(uint64(user.ID)+1000, 10)
		if err := tx.Accounts().Create(&models.Account{ID: user.AccountID, UserID: user.ID}); err != nil {
			return err
		}
		return tx.Users().SetAccount(&user)
	})
	if errors.Is(err, repository.ErrDuplicate) {
		err = Fail(409, "conflict", "username already exists")
	}
	return user, err
}
func (s *Service) Verify(ctx context.Context, input Credentials) (models.User, error) {
	input.Username = strings.TrimSpace(input.Username)
	var user models.User
	invalid := func() (models.User, error) { return user, Fail(401, "unauthorized", "invalid username or password") }
	if n := utf8.RuneCountInString(input.Username); n < 1 || n > 64 || len(input.Password) < 8 || len(input.Password) > 72 {
		return invalid()
	}
	found, err := s.Store.WithContext(ctx).Users().ByUsername(input.Username)
	if err == nil {
		user = *found
	}
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return user, err
	}
	hash := s.dummy
	if err == nil {
		hash = []byte(user.Password)
	}
	comparison := bcrypt.CompareHashAndPassword(hash, []byte(input.Password))
	if err != nil || comparison != nil {
		return invalid()
	}
	return user, nil
}
func (s *Service) User(ctx context.Context, id uint) (models.User, error) {
	user, err := s.Store.WithContext(ctx).Users().ByID(id)
	if errors.Is(err, repository.ErrNotFound) {
		err = Fail(404, "unsupported_action", "user not found")
	}
	if err != nil {
		return models.User{}, err
	}
	return *user, err
}
func (s *Service) Users(ctx context.Context) ([]models.User, error) {
	return s.Store.WithContext(ctx).Users().List()
}
func lookup(store repository.Store, id uint, account string) (*models.Account, error) {
	a, err := store.Accounts().ByOwner(account, id)
	if errors.Is(err, repository.ErrNotFound) {
		err = Fail(404, "unknown_account", "Account not found")
	}
	return a, err
}
func (s *Service) Balance(ctx context.Context, id uint, account string) (*models.Account, error) {
	return lookup(s.Store.WithContext(ctx), id, account)
}

// Change owns the success audit; callers own failed-operation audits after rollback.
func (s *Service) Change(ctx context.Context, id uint, account string, amount int64, withdraw bool, audit models.AuditLog) (*models.Account, error) {
	if amount <= 0 {
		return nil, Fail(400, "invalid_amount", "Amount must be a positive int64 integer")
	}
	var result *models.Account
	err := s.Store.WithContext(ctx).Transaction(func(tx repository.Store) error {
		_, err := lookup(tx, id, account)
		if err != nil {
			return err
		}
		if err := tx.Accounts().ChangeBalance(account, id, amount, withdraw); err != nil {
			switch {
			case errors.Is(err, repository.ErrInsufficientFunds):
				return Fail(409, "insufficient_funds", "Account does not have enough balance")
			case errors.Is(err, repository.ErrBalanceOverflow):
				return Fail(400, "invalid_amount", "Deposit would exceed maximum balance")
			case errors.Is(err, repository.ErrNotFound):
				return Fail(404, "unknown_account", "Account not found")
			default:
				return err
			}
		}
		result, err = lookup(tx, id, account)
		if err != nil {
			return err
		}
		audit.UserID = &id
		audit.Amount = &amount
		audit.AccountID = account
		audit.Result = "success"
		audit.StatusCode = 200
		audit.Balance = &result.Balance
		if withdraw {
			audit.Action = "withdraw"
		} else {
			audit.Action = "deposit"
		}
		return tx.AuditLogs().Create(&audit)
	})
	return result, err
}
