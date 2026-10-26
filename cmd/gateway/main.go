package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/coordinator"
	"github.com/azare242/gobank/internal/gateway"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/runtimeconfig"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	ctx, cancel := runtimeconfig.Context()
	defer cancel()
	startup, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	m, secret, e := runtimeconfig.Config()
	if e != nil {
		return e
	}
	tokens, e := auth.FromEnv()
	if e != nil {
		return e
	}
	password := os.Getenv("FIXTURE_PASSWORD")
	if len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("FIXTURE_PASSWORD must contain 8 to 72 bytes")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	store, e := repository.OpenSQL(startup, os.Getenv("COORDINATOR_DSN"))
	if e != nil {
		return e
	}
	defer store.DB.Close()
	pool := gateway.NewPool(m, secret)
	c := &coordinator.Coordinator{DB: store.DB, Map: m, Transport: pool}
	if e = c.Migrate(startup); e != nil {
		return e
	}
	// Explicit opt-in crash injection for reproducible recovery experiments.
	if point := os.Getenv("CRASH_AT"); point != "" {
		c.Crash = func(p string) {
			if point == p {
				os.Exit(86)
			}
		}
	}
	pool.Probe(startup)
	go pool.Run(ctx)
	go c.Run(ctx)
	return runtimeconfig.Serve(ctx, (&gateway.Server{Pool: pool, Coordinator: c, Tokens: tokens, FixturePasswordHash: hash}).Handler())
}
