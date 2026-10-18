package bankclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryPreservesIDAfterLostResponse(t *testing.T) {
	var effects atomic.Int32
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "operation-1" {
			t.Error("ID changed")
		}
		if requests.Add(1) == 1 {
			effects.Add(1)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		fmt.Fprint(w, `{"status":"success","operation_id":"operation-1","balance":100}`)
	}))
	defer s.Close()
	c, err := New(s.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := c.Do(ctx, "POST", "/deposit", "operation-1", []byte(`{"amount":100}`))
	if err != nil || r.Balance != 100 || effects.Load() != 1 || requests.Load() < 2 {
		t.Fatal(r, err, effects.Load(), requests.Load())
	}
}
func TestUnavailableDeadlineIsUnknown(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"status":"error","error":"unavailable"}`)
	}))
	defer s.Close()
	c, _ := New(s.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.Do(ctx, "POST", "/deposit", "same-id", nil); err == nil {
		t.Fatal("must return unknown outcome")
	}
}
