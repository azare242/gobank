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
	"github.com/azare242/gobank/internal/shardserver"
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
	store, e := repository.OpenSQL(startup, os.Getenv("DB_DSN"))
	if e != nil {
		return e
	}
	defer store.DB.Close()
	if e = store.Migrate(startup); e != nil {
		return e
	}
	if os.Getenv("SEED_FIXTURES") == "true" {
		if e = store.Seed(startup, shard.First, shard.Last); e != nil {
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
	if e = p.Ready(startup); e != nil {
		return e
	}
	s := &shardserver.Server{Store: store, Map: m, ShardID: shard.ID, ReplicaID: replica, Secret: secret, Participant: p}
	return runtimeconfig.Serve(ctx, s.Handler())
}
