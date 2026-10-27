package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/azare242/gobank/internal/healing"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	project := flag.String("project", os.Getenv("HEALING_PROJECT"), "Compose project")
	state := flag.String("state", "/data/state.json", "persistent controller state")
	services := flag.String("services", env("HEALING_SERVICES", "shard-a1,shard-a2,shard-b1,shard-b2"), "comma-separated desired slots")
	socket := flag.String("socket", "/var/run/docker.sock", "Docker Engine socket")
	interval := flag.Duration("interval", time.Second, "reconciliation interval")
	grace := flag.Duration("grace", 10*time.Second, "unhealthy grace period")
	crash := flag.String("crash-at", os.Getenv("HEALING_CRASH_AT"), "opt-in after-create crash")
	flag.Parse()
	if *interval <= 0 {
		return &argumentError{"interval must be positive"}
	}
	if *crash != "" && *crash != "after-create" {
		return &argumentError{"unknown crash point"}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	c, e := healing.Open(*state, healing.NewDocker(*socket), *grace)
	if e != nil {
		return e
	}
	defer c.Close()
	if e = c.Bootstrap(ctx, *project, strings.Split(*services, ",")); e != nil {
		return e
	}
	c.Crash = func(point string) {
		if *crash == point {
			os.Exit(86)
		}
	}
	c.Event = func(event, slot string) { log.Printf("event=%s slot=%s", event, slot) }
	log.Printf("event=controller-ready project=%s", *project)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		if e = c.Reconcile(ctx, time.Now()); e != nil {
			log.Printf("reconcile: %v", e)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type argumentError struct{ message string }

func (e *argumentError) Error() string { return e.message }

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
