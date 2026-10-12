package database

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/azare242/gobank/internal/models"
)

func Open(path string) (*gorm.DB, error) {
	return OpenConfig(Config{Driver: "sqlite", DSN: path})
}

type Config struct {
	Driver string
	DSN    string
}

// OpenConfig connects and migrates the same models on either supported dialect.
func OpenConfig(config Config) (*gorm.DB, error) {
	var dialector gorm.Dialector
	driver := strings.ToLower(strings.TrimSpace(config.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite":
		if config.DSN == "" {
			config.DSN = "users.db"
		}
		dialector = sqlite.Open(config.DSN)
	case "postgres", "postgresql":
		if strings.TrimSpace(config.DSN) == "" {
			return nil, fmt.Errorf("PostgreSQL requires DB_DSN")
		}
		dialector = postgres.Open(config.DSN)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", config.Driver)
	}
	db, err := gorm.Open(dialector, &gorm.Config{
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
	if driver == "sqlite" {
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
	}
	if err := db.AutoMigrate(&models.User{}, &models.Account{}, &models.AuditLog{}, &models.Session{}); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}
