// Package settings reads optional environment settings shared by the services.
package settings

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

func Duration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		slog.Warn("invalid duration; using default", "setting", key)
		return fallback
	}
	return value
}
func Int(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		slog.Warn("invalid integer; using default", "setting", key)
		return fallback
	}
	return value
}
