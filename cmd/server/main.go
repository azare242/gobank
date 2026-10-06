package main

import (
	"log"
	"os"

	"github.com/azare242/gobank/internal/api"
	"github.com/azare242/gobank/internal/database"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "users.db"
	}
	address := os.Getenv("LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:3000"
	}
	db, err := database.Open(path)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return api.New(db).Listen(address)
}
