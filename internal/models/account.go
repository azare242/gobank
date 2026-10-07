package models

type Account struct {
	ID      string `gorm:"primaryKey" json:"id"`
	UserID  uint   `gorm:"not null;uniqueIndex" json:"-"`
	Balance int64  `gorm:"not null;check:balance >= 0" json:"balance"`
}
