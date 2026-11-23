package runtimeconfig

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRetryRecoversAndCancels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	attempts := 0
	err := Retry(ctx, "test", func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporarily unavailable")
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	err = Retry(canceled, "test", func(context.Context) error { t.Fatal("called after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestRetryBoundsAttempt(t *testing.T) {
	t.Setenv("STARTUP_ATTEMPT_TIMEOUT", "5ms")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := Retry(ctx, "blocked", func(attempt context.Context) error { <-attempt.Done(); return attempt.Err() })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestShutdownWaitsForActiveRequest(t *testing.T) {
	t.Setenv("SHUTDOWN_TIMEOUT", "2s")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(204) })}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, func() error { return server.Serve(listener) }) }()
	response := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if resp != nil {
			resp.Body.Close()
		}
		response <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server returned with active request: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
