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
	"github.com/azare242/gobank/internal/settings"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	runtimeconfig.Logging("gateway")
	ctx, cancel := runtimeconfig.Context()
	defer cancel()
	startup, stop := context.WithTimeout(ctx, settings.Duration("STARTUP_TIMEOUT", 2*time.Minute))
	defer stop()
	m, secret, e := runtimeconfig.Config()
	if e != nil {
		return e
	}
	closeRouting, e := runtimeconfig.Routing(startup, m)
	if e != nil {
		return e
	}
	defer closeRouting()
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
	var store *repository.SQLStore
	e = runtimeconfig.Retry(startup, "database connection", func(ctx context.Context) error {
		var err error
		store, err = repository.OpenSQL(ctx, os.Getenv("COORDINATOR_DSN"))
		return err
	})
	if e != nil {
		return e
	}
	defer store.DB.Close()
	pool := gateway.NewPool(m, secret)
	c := &coordinator.Coordinator{DB: store.DB, Map: m, Transport: pool}
	if e = runtimeconfig.Retry(startup, "coordinator migration", c.Migrate); e != nil {
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
	closeMetrics, e := runtimeconfig.Metrics(ctx, store.DB, "gateway", func(ctx context.Context) bool { return pool.ReadyContext(ctx) })
	if e != nil {
		return e
	}
	defer closeMetrics()
	workers, stopWorkers := context.WithCancel(ctx)
	poolDone, recoveryDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(poolDone); pool.Run(workers) }()
	go func() { defer close(recoveryDone); c.Run(workers) }()
	defer func() { stopWorkers(); <-poolDone; <-recoveryDone }()
	return runtimeconfig.Serve(ctx, (&gateway.Server{Pool: pool, Coordinator: c, Tokens: tokens, FixturePasswordHash: hash}).Handler())
}
