package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/azare242/gobank/internal/participant"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/runtimeconfig"
	"github.com/azare242/gobank/internal/settings"
	"github.com/azare242/gobank/internal/shardserver"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	runtimeconfig.Logging("shard")
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
	shard, e := m.ByID(os.Getenv("SHARD_ID"))
	if e != nil {
		return e
	}
	replica := os.Getenv("REPLICA_ID")
	if replica == "" {
		return fmt.Errorf("REPLICA_ID is required")
	}
	var store *repository.SQLStore
	e = runtimeconfig.Retry(startup, "database connection", func(ctx context.Context) error {
		var err error
		store, err = repository.OpenSQL(ctx, os.Getenv("DB_DSN"))
		return err
	})
	if e != nil {
		return e
	}
	defer store.DB.Close()
	if e = runtimeconfig.Retry(startup, "shard migration", store.Migrate); e != nil {
		return e
	}
	if os.Getenv("SEED_FIXTURES") == "true" {
		if e = runtimeconfig.Retry(startup, "fixture seed", func(ctx context.Context) error { return store.Seed(ctx, shard.First, shard.Last) }); e != nil {
			return e
		}
	}
	p := &participant.Participant{Store: store, Map: m, ShardID: shard.ID}
	if point := os.Getenv("CRASH_AT"); point != "" {
		p.Crash = func(at string) {
			if point == at {
				os.Exit(86)
			}
		}
	}
	if e = runtimeconfig.Retry(startup, "participant readiness", p.Ready); e != nil {
		return e
	}
	closeMetrics, e := runtimeconfig.Metrics(ctx, store.DB, "shard", func(ctx context.Context) bool { return p.Ready(ctx) == nil })
	if e != nil {
		return e
	}
	defer closeMetrics()
	s := &shardserver.Server{Store: store, Map: m, ShardID: shard.ID, ReplicaID: replica, Secret: secret, Participant: p}
	return runtimeconfig.Serve(ctx, s.Handler())
}
