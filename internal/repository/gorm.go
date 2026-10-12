package repository

import (
	"context"
	"errors"
	"math"

	"github.com/azare242/gobank/internal/models"
	"gorm.io/gorm"
)

type gormStore struct{ db *gorm.DB }
type users struct{ db *gorm.DB }
type accounts struct{ db *gorm.DB }
type auditLogs struct{ db *gorm.DB }

func NewGORM(db *gorm.DB) Store                            { return &gormStore{db: db} }
func (s *gormStore) Users() Users                          { return &users{s.db} }
func (s *gormStore) Accounts() Accounts                    { return &accounts{s.db} }
func (s *gormStore) AuditLogs() AuditLogs                  { return &auditLogs{s.db} }
func (s *gormStore) WithContext(ctx context.Context) Store { return NewGORM(s.db.WithContext(ctx)) }
func (s *gormStore) Transaction(fn func(Store) error) error {
	return translate(s.db.Transaction(func(tx *gorm.DB) error { return fn(NewGORM(tx)) }))
}
func translate(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}
func (r *users) Create(user *models.User) error { return translate(r.db.Create(user).Error) }
func (r *users) SetAccount(user *models.User) error {
	result := r.db.Model(&models.User{}).Where("id = ?", user.ID).Update("account_id", user.AccountID)
	if result.Error != nil {
		return translate(result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
func (r *users) ByID(id uint) (*models.User, error) {
	var user models.User
	err := r.db.Select("id", "username", "account_id").Where("id = ?", id).First(&user).Error
	return &user, translate(err)
}
func (r *users) ByUsername(name string) (*models.User, error) {
	var user models.User
	err := r.db.Where("username = ?", name).First(&user).Error
	return &user, translate(err)
}
func (r *users) List() ([]models.User, error) {
	result := make([]models.User, 0)
	err := r.db.Select("id", "username", "account_id").Order("id ASC").Find(&result).Error
	return result, translate(err)
}
func (r *accounts) Create(account *models.Account) error {
	return translate(r.db.Create(account).Error)
}
func (r *accounts) ByOwner(id string, owner uint) (*models.Account, error) {
	var account models.Account
	err := r.db.Where("id = ? AND user_id = ?", id, owner).First(&account).Error
	return &account, translate(err)
}
func (r *accounts) ChangeBalance(id string, owner uint, amount int64, withdraw bool) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	// Ownership and balance guards are part of the atomic UPDATE on both dialects.
	query := r.db.Model(&models.Account{}).Where("id = ? AND user_id = ?", id, owner)
	if withdraw {
		query = query.Where("balance >= ?", amount).UpdateColumn("balance", gorm.Expr("balance - ?", amount))
	} else {
		query = query.Where("balance <= ?", math.MaxInt64-amount).UpdateColumn("balance", gorm.Expr("balance + ?", amount))
	}
	if query.Error != nil {
		return translate(query.Error)
	}
	if query.RowsAffected == 0 {
		if _, err := r.ByOwner(id, owner); err != nil {
			return err
		}
		if withdraw {
			return ErrInsufficientFunds
		}
		return ErrBalanceOverflow
	}
	return nil
}
func (r *auditLogs) Create(entry *models.AuditLog) error { return translate(r.db.Create(entry).Error) }
