package models

import "time"

type Session struct {
	ID        uint64    `gorm:"primaryKey"`
	TokenHash string    `gorm:"not null;uniqueIndex"`
	UserID    uint      `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
}
