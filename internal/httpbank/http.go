// Package httpbank contains shared HTTP boundaries for Assignment 2 services.
package httpbank

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/google/uuid"
)

type requestIDKey struct{}

func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func Error(w http.ResponseWriter, e error) {
	status, code := 503, "temporarily_unavailable"
	var app *serviceerr.Error
	if errors.As(e, &app) {
		status, code = app.Status, app.Code
	}
	JSON(w, status, map[string]string{"status": "error", "error": code})
}
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return serviceerr.New(400, "invalid_json")
	}
	if e := d.Decode(new(any)); e != io.EOF {
		return serviceerr.New(400, "invalid_json")
	}
	return nil
}
func Identity(tokens *auth.Service, r *http.Request) (uint, error) {
	p := strings.Fields(r.Header.Get("Authorization"))
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") || len(p[1]) > 4096 {
		return 0, serviceerr.New(401, "unauthorized")
	}
	id, e := tokens.Validate(p[1])
	if e != nil {
		return 0, serviceerr.New(401, "unauthorized")
	}
	return id, nil
}
func Internal(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Token")), []byte(secret)) != 1 {
			Error(w, serviceerr.New(401, "unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func Log(shard, replica string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if len(id) == 0 || len(id) > 200 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		if replica != "" {
			w.Header().Set("X-Bank-Replica", replica)
		}
		if tx := r.Header.Get("X-Transaction-ID"); tx != "" {
			w.Header().Set("X-Transaction-ID", tx)
		}
		start := time.Now()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
		slog.Info("request", "shard_id", shard, "replica_id", replica, "request_id", id, "operation_id", r.Header.Get("Idempotency-Key"), "transaction_id", w.Header().Get("X-Transaction-ID"), "path", r.URL.Path, "duration", time.Since(start))
	})
}
