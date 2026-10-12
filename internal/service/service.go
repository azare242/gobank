// Package service contains banking rules independent of transports and authentication mechanisms.
package service

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/azare242/gobank/internal/models"
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
	DB    *gorm.DB
	dummy []byte
}

func New(db *gorm.DB) *Service {
	hash, err := bcrypt.GenerateFromPassword([]byte("dummy-login-comparison"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return &Service{db, hash}
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
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		user.AccountID = "A" + strconv.FormatUint(uint64(user.ID)+1000, 10)
		if err := tx.Create(&models.Account{ID: user.AccountID, UserID: user.ID}).Error; err != nil {
			return err
		}
		return tx.Model(&user).Update("account_id", user.AccountID).Error
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
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
	err := s.DB.WithContext(ctx).Where("username = ?", input.Username).First(&user).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
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
	var user models.User
	err := s.DB.WithContext(ctx).Select("id", "username", "account_id").First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = Fail(404, "unsupported_action", "user not found")
	}
	return user, err
}
func (s *Service) Users(ctx context.Context) ([]models.User, error) {
	users := make([]models.User, 0)
	err := s.DB.WithContext(ctx).Select("id", "username", "account_id").Order("id ASC").Find(&users).Error
	return users, err
}
func lookup(db *gorm.DB, id uint, account string) (*models.Account, error) {
	var a models.Account
	err := db.Where("id = ? AND user_id = ?", account, id).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = Fail(404, "unknown_account", "Account not found")
	}
	return &a, err
}
func (s *Service) Balance(ctx context.Context, id uint, account string) (*models.Account, error) {
	return lookup(s.DB.WithContext(ctx), id, account)
}

// Change owns the success audit; callers own failed-operation audits after rollback.
func (s *Service) Change(ctx context.Context, id uint, account string, amount int64, withdraw bool, audit models.AuditLog) (*models.Account, error) {
	if amount <= 0 {
		return nil, Fail(400, "invalid_amount", "Amount must be a positive int64 integer")
	}
	var result *models.Account
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		a, err := lookup(tx, id, account)
		if err != nil {
			return err
		}
		query := tx.Model(&models.Account{}).Where("id = ?", a.ID)
		if withdraw {
			query = query.Where("balance >= ?", amount).UpdateColumn("balance", gorm.Expr("balance - ?", amount))
		} else {
			query = query.Where("balance <= ?", math.MaxInt64-amount).UpdateColumn("balance", gorm.Expr("balance + ?", amount))
		}
		if query.Error != nil {
			return query.Error
		}
		if query.RowsAffected == 0 {
			if withdraw {
				return Fail(409, "insufficient_funds", "Account does not have enough balance")
			}
			return Fail(400, "invalid_amount", "Deposit would exceed maximum balance")
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
		return tx.Create(&audit).Error
	})
	return result, err
}
