package bankclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTransferReconcilesPending(t *testing.T) {
	var mutations, polls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/transfers" {
			mutations.Add(1)
			if r.Header.Get("Idempotency-Key") != "same-key" {
				t.Error("lost key")
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"status":"pending","pending":true,"transaction_id":"tx"}`))
			return
		}
		if polls.Add(1) < 2 {
			_, _ = w.Write([]byte(`{"status":"COMMIT_DECIDED","pending":true,"transaction_id":"tx","decision":"COMMIT"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"COMPLETED","transaction_id":"tx","decision":"COMMIT","outcome":{"status":"success"}}`))
	}))
	defer s.Close()
	c, _ := New(s.URL, "token")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, e := c.Transfer(ctx, "same-key", "A0500", "A1500", 300)
	if e != nil || r.Status != "success" || r.TransactionID != "tx" || mutations.Load() != 1 || polls.Load() != 2 {
		t.Fatalf("%+v %v mutations=%d polls=%d", r, e, mutations.Load(), polls.Load())
	}
}

func TestExpiredAuthenticationKeepsTransferPending(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/transfers" {
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"status":"pending","pending":true,"transaction_id":"tx"}`))
			return
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"status":"error","error":"unauthorized"}`))
	}))
	defer s.Close()
	c, _ := New(s.URL, "expired")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, e := c.Transfer(ctx, "key", "A0500", "A1500", 300)
	if e == nil || !r.Pending || r.TransactionID != "tx" {
		t.Fatalf("uncertain transfer became terminal: %+v %v", r, e)
	}
}
