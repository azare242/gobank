package database

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/azare242/gobank/internal/models"
)

func Open(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get database connection: %w", err)
	}
	// Serialize writes for this small SQLite application.
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&models.User{}); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}
