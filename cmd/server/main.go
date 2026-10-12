package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/azare242/gobank/internal/api"
	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/repository"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	maxConcurrent := 100
	if raw := os.Getenv("MAX_CONCURRENT_REQUESTS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return fmt.Errorf("MAX_CONCURRENT_REQUESTS must be a positive integer")
		}
		maxConcurrent = value
	}
	tokens, err := auth.FromEnv()
	if err != nil {
		return err
	}
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "users.db"
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:3000"
	}
	dsn := os.Getenv("DB_DSN")
	driver := strings.ToLower(strings.TrimSpace(os.Getenv("DB_DRIVER")))
	if dsn == "" && (driver == "" || driver == "sqlite") {
		dsn = path
	}
	db, err := database.OpenConfig(database.Config{Driver: driver, DSN: dsn})
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return api.NewWithRepository(repository.NewGORM(db), tokens, api.Config{MaxConcurrentRequests: maxConcurrent}).Listen(address)
}
