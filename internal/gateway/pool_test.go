package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/azare242/gobank/internal/shardmap"
)

func TestRoundRobinAndInterruption(t *testing.T) {
	var bad atomic.Bool
	replica := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id == "a1" && bad.Load() {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("X-Bank-Replica", id)
			_, _ = w.Write([]byte(`{"status":"success"}`))
		}))
	}
	a1, a2, b := replica("a1"), replica("a2"), replica("b")
	defer a1.Close()
	defer a2.Close()
	defer b.Close()
	m := &shardmap.Map{Shards: []shardmap.Shard{{ID: "a", Replicas: []string{a1.URL, a2.URL}}, {ID: "b", Replicas: []string{b.URL}}}}
	p := NewPool(m, "test")
	p.Probe(context.Background())
	seen := map[string]int{}
	for i := 0; i < 10; i++ {
		var out any
		id, e := p.Call(context.Background(), "a", "/test", nil, &out)
		if e != nil {
			t.Fatal(e)
		}
		seen[id]++
	}
	if seen["a1"] != 5 || seen["a2"] != 5 || seen["b"] != 0 {
		t.Fatal(seen)
	}
	bad.Store(true)
	p.Probe(context.Background())
	for i := 0; i < 3; i++ {
		var out any
		id, e := p.Call(context.Background(), "a", "/test", nil, &out)
		if e != nil || id != "a2" {
			t.Fatalf("%s %v", id, e)
		}
	}
	bad.Store(false)
	p.Probe(context.Background())
	seen = map[string]int{}
	for i := 0; i < 4; i++ {
		var out any
		id, e := p.Call(context.Background(), "a", "/test", nil, &out)
		if e != nil {
			t.Fatal(e)
		}
		seen[id]++
	}
	if seen["a1"] != 2 {
		t.Fatal(seen)
	}
}
