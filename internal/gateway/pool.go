// Package gateway routes requests only to healthy replicas of their owning shard.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/azare242/gobank/internal/httpbank"
	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/azare242/gobank/internal/shardmap"
)

type Pool struct {
	Map     *shardmap.Map
	Secret  string
	HTTP    *http.Client
	mu      sync.Mutex
	healthy map[string]bool
	next    map[string]int
}

func NewPool(m *shardmap.Map, secret string) *Pool {
	return &Pool{Map: m, Secret: secret, HTTP: &http.Client{Timeout: 6 * time.Second}, healthy: map[string]bool{}, next: map[string]int{}}
}
func (p *Pool) Probe(ctx context.Context) {
	var wg sync.WaitGroup
	shards, _, e := p.Map.Current()
	if e != nil {
		return
	}
	for _, s := range shards {
		for _, base := range s.Replicas {
			wg.Add(1)
			go func(base string) {
				defer wg.Done()
				c, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				req, _ := http.NewRequestWithContext(c, "GET", base+"/ready", nil)
				resp, e := p.HTTP.Do(req)
				ok := e == nil && resp.StatusCode == 200
				if resp != nil {
					resp.Body.Close()
				}
				p.mu.Lock()
				p.healthy[base] = ok
				p.mu.Unlock()
			}(base)
		}
	}
	wg.Wait()
}
func (p *Pool) Run(ctx context.Context) {
	p.Probe(ctx)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Probe(ctx)
		}
	}
}
func (p *Pool) Ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	shards, _, e := p.Map.Current()
	if e != nil {
		return false
	}
	for _, s := range shards {
		if s.First == 0 && s.Last == 0 {
			continue
		}
		ok := false
		for _, u := range s.Replicas {
			ok = ok || p.healthy[u]
		}
		if !ok {
			return false
		}
	}
	return true
}
func (p *Pool) candidates(shard string) ([]string, error) {
	s, e := p.Map.ByID(shard)
	if e != nil {
		return nil, e
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var healthy []string
	for _, u := range s.Replicas {
		if p.healthy[u] {
			healthy = append(healthy, u)
		}
	}
	if len(healthy) == 0 {
		return nil, serviceerr.New(503, "no_healthy_replica")
	}
	n := p.next[shard] % len(healthy)
	p.next[shard]++
	return append(healthy[n:], healthy[:n]...), nil
}

// Call retries uncertain deliveries only within the logical shard. Every write
// endpoint accepts durable operation identities, so a lost reply is safe to retry.
func (p *Pool) Call(ctx context.Context, shard, path string, input, output any) (string, error) {
	candidates, e := p.candidates(shard)
	if e != nil {
		return "", e
	}
	b, e := json.Marshal(input)
	if e != nil {
		return "", e
	}
	for _, base := range candidates {
		req, e := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(b))
		if e != nil {
			return "", e
		}
		_, epoch, err := p.Map.Current()
		if err != nil {
			return "", err
		}
		req.Header.Set("X-Shard-Epoch", fmt.Sprint(epoch))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", p.Secret)
		req.Header.Set("X-Request-ID", httpbank.RequestID(ctx))
		var identity struct {
			Key     string `json:"key"`
			ID      string `json:"id"`
			Command struct {
				ID string `json:"id"`
			} `json:"command"`
		}
		_ = json.Unmarshal(b, &identity)
		req.Header.Set("Idempotency-Key", identity.Key)
		transaction := identity.ID
		if transaction == "" {
			transaction = identity.Command.ID
		}
		req.Header.Set("X-Transaction-ID", transaction)
		resp, e := p.HTTP.Do(req)
		if e != nil {
			p.mu.Lock()
			p.healthy[base] = false
			p.mu.Unlock()
			continue
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if e != nil {
			continue
		}
		if resp.StatusCode >= 500 {
			p.mu.Lock()
			p.healthy[base] = false
			p.mu.Unlock()
			continue
		}
		if resp.StatusCode >= 400 {
			var problem struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(data, &problem)
			return resp.Header.Get("X-Bank-Replica"), serviceerr.New(resp.StatusCode, problem.Error)
		}
		if e = json.Unmarshal(data, output); e != nil {
			return "", fmt.Errorf("invalid replica response: %w", e)
		}
		return resp.Header.Get("X-Bank-Replica"), nil
	}
	return "", serviceerr.New(503, "shard_unavailable")
}
