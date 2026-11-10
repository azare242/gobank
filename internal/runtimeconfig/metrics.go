package runtimeconfig

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/azare242/gobank/internal/observability"
	"github.com/azare242/gobank/internal/settings"
)

// Metrics is disabled unless METRICS_ADDR is set. Queries run in the background,
// so a scrape never waits for an unavailable database.
func Metrics(ctx context.Context, db *sql.DB, service string, ready func(context.Context) bool) (func(), error) {
	addr := os.Getenv("METRICS_ADDR")
	if addr == "" {
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", observability.Handler())
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second}
	worker, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics listener failed")
		}
	}()
	observability.Set("gobank_build_info", observability.Labels("version", Env("VERSION", "dev"), "commit", Env("COMMIT", "unknown")), 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			sample, stop := context.WithTimeout(worker, settings.Duration("READINESS_TIMEOUT", time.Second))
			up := db.PingContext(sample) == nil
			labels := observability.Labels("shard", os.Getenv("SHARD_ID"))
			name := "gobank_db_up"
			if service == "gateway" {
				name = "gobank_coordinator_db_up"
				labels = ""
			}
			observability.Set(name, labels, boolean(up))
			observability.Set("gobank_ready", observability.Labels("service", service, "shard", os.Getenv("SHARD_ID"), "replica", os.Getenv("REPLICA_ID")), boolean(up && ready(sample)))
			if up {
				sampleDatabase(sample, db, service)
			}
			stop()
			select {
			case <-worker.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); _ = server.Close(); <-done }, nil
}
func boolean(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func sampleDatabase(ctx context.Context, db *sql.DB, service string) {
	if service == "shard" {
		var n float64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_prepared_xacts WHERE database=current_database()`).Scan(&n); err == nil {
			observability.Set("gobank_prepared_transactions", observability.Labels("shard", os.Getenv("SHARD_ID")), n)
		}
		return
	}
	rows, err := db.QueryContext(ctx, `SELECT status,decision,count(*) FROM bank_transactions GROUP BY status,decision`)
	if err != nil {
		return
	}
	counts := map[string]float64{}
	for rows.Next() {
		var status, decision string
		var count float64
		if err = rows.Scan(&status, &decision, &count); err != nil {
			break
		}
		counts[observability.Labels("status", status, "decision", decision)] = count
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		return
	}
	for _, status := range []string{"INITIATED", "PREPARING", "PREPARED", "COMMIT_DECIDED", "ABORT_DECIDED", "COMPLETED"} {
		for _, decision := range []string{"", "COMMIT", "ABORT"} {
			labels := observability.Labels("status", status, "decision", decision)
			observability.Set("gobank_transactions", labels, counts[labels])
		}
	}
	var pending, oldest float64
	err = db.QueryRowContext(ctx, `SELECT count(*),COALESCE(EXTRACT(EPOCH FROM now()-min(updated_at)),0) FROM bank_transactions WHERE status<>'COMPLETED'`).Scan(&pending, &oldest)
	if err == nil {
		observability.Set("gobank_transactions_pending", "", pending)
		observability.Set("gobank_transaction_oldest_pending_seconds", "", oldest)
	}
}
