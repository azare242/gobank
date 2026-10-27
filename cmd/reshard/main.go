// reshard resumes the fixed Assignment 2 bonus migration; reruns are idempotent.
package main

import (
	"context"
	"fmt"
	"github.com/azare242/gobank/internal/coordinator"
	"github.com/azare242/gobank/internal/gateway"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/reshard"
	"github.com/azare242/gobank/internal/runtimeconfig"
	"log"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	ctx, cancel := runtimeconfig.Context()
	defer cancel()
	m, secret, e := runtimeconfig.Config()
	if e != nil {
		return e
	}
	closeRouting, e := runtimeconfig.Routing(ctx, m)
	if e != nil {
		return e
	}
	defer closeRouting()
	if m.DB == nil {
		return fmt.Errorf("ROUTING_DSN is required")
	}
	source, e := repository.OpenSQL(ctx, os.Getenv("SOURCE_DSN"))
	if e != nil {
		return e
	}
	defer source.DB.Close()
	target, e := repository.OpenSQL(ctx, os.Getenv("TARGET_DSN"))
	if e != nil {
		return e
	}
	defer target.DB.Close()
	if e = target.Migrate(ctx); e != nil {
		return e
	}
	pool := gateway.NewPool(m, secret)
	pool.Probe(ctx)
	go pool.Run(ctx)
	c := &coordinator.Coordinator{DB: m.DB, Map: m, Transport: pool}
	if e = c.Migrate(ctx); e != nil {
		return e
	}
	w := &reshard.Worker{Metadata: m.DB, Source: source.DB, Target: target.DB, Map: m, Coordinator: c}
	if point := os.Getenv("MIGRATION_CRASH_AT"); point != "" {
		w.Crash = func(at string) {
			if at == point {
				os.Exit(86)
			}
		}
	}
	for {
		task, stop := context.WithTimeout(ctx, 30*time.Second)
		e = w.Run(task)
		stop()
		if e == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Printf("migration will resume: %v", e)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
