// Package runtimeconfig shares startup validation and graceful HTTP shutdown.
package runtimeconfig

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/reshard"
	"github.com/azare242/gobank/internal/settings"
	"github.com/azare242/gobank/internal/shardmap"
)

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func Config() (*shardmap.Map, string, error) {
	m, e := shardmap.Load(Env("SHARD_MAP", "deploy/sharded/shards.json"))
	if e != nil {
		return nil, "", e
	}
	secret := os.Getenv("INTERNAL_TOKEN")
	if len(secret) < 32 {
		return nil, "", fmt.Errorf("INTERNAL_TOKEN must contain at least 32 bytes")
	}
	return m, secret, nil
}
func Context() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func Serve(ctx context.Context, handler http.Handler) error {
	server := &http.Server{Addr: Env("LISTEN_ADDR", ":3000"), Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: settings.Duration("HTTP_READ_TIMEOUT", 10*time.Second), WriteTimeout: settings.Duration("HTTP_WRITE_TIMEOUT", 60*time.Second), IdleTimeout: 60 * time.Second}
	return serve(ctx, server, server.ListenAndServe)
}

func serve(ctx context.Context, server *http.Server, listen func() error) error {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), settings.Duration("SHUTDOWN_TIMEOUT", 25*time.Second))
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				slog.Warn("HTTP shutdown timed out")
				_ = server.Close()
			}
		case <-done:
		}
	}()
	e := listen()
	close(done)
	<-stopped
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}

// Routing is opt-in; the original static deployment remains supported.
func Routing(ctx context.Context, m *shardmap.Map) (func(), error) {
	dsn := os.Getenv("ROUTING_DSN")
	if dsn == "" {
		return func() {}, nil
	}
	store, e := repository.OpenSQL(ctx, dsn)
	if e != nil {
		return nil, e
	}
	if e = reshard.Initialize(ctx, store.DB, m); e != nil {
		store.DB.Close()
		return nil, e
	}
	m.DB = store.DB
	return func() { store.DB.Close() }, nil
}

// Retry waits for dependencies during startup, bounded by the caller's context.
func Retry(ctx context.Context, step string, fn func(context.Context) error) error {
	delay := 250 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w", step, err)
		}
		attempt, cancel := context.WithTimeout(ctx, settings.Duration("STARTUP_ATTEMPT_TIMEOUT", 10*time.Second))
		err := fn(attempt)
		cancel()
		if err == nil {
			return nil
		}
		slog.Warn("startup step failed; retrying", "step", step)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w", step, ctx.Err())
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}
func Logging(service string) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service, "shard", os.Getenv("SHARD_ID"), "replica", os.Getenv("REPLICA_ID"), "version", Env("VERSION", "dev")))
}
