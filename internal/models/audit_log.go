package models

import "time"

// AuditLog records authentication attempts and money movements, without credentials.
type AuditLog struct {
	ID         uint64    `gorm:"primaryKey"`
	CreatedAt  time.Time `gorm:"not null;index"`
	RequestID  string    `gorm:"not null;index"`
	ClientIP   string    `gorm:"not null"`
	UserID     *uint     `gorm:"index"`
	Username   string
	AccountID  string `gorm:"index"`
	Action     string `gorm:"not null;index"`
	Method     string `gorm:"not null"`
	Path       string `gorm:"not null"`
	Result     string `gorm:"not null"`
	StatusCode int    `gorm:"not null"`
	ErrorCode  string
	Amount     *int64
	Balance    *int64
}
