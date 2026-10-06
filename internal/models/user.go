package models

// User stores a bcrypt password hash, which is never included in JSON responses.
type User struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	Username string `gorm:"not null;uniqueIndex" json:"username"`
	Password string `gorm:"not null" json:"-"`
}
