// Package runtimeconfig shares startup validation and graceful HTTP shutdown.
package runtimeconfig

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	server := &http.Server{Addr: Env("LISTEN_ADDR", ":3000"), Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	e := server.ListenAndServe()
	close(done)
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
