package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/azare242/gobank/internal/shardmap"
)

type fakeResolver struct {
	hosts []string
	err   error
}

func (r *fakeResolver) LookupHost(context.Context, string) ([]string, error) { return r.hosts, r.err }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDNSScalingAndFailures(t *testing.T) {
	resolver := &fakeResolver{hosts: []string{"10.0.0.2", "10.0.0.1", "10.0.0.1"}}
	m := &shardmap.Map{Shards: []shardmap.Shard{{ID: "a", First: 1, Last: 2000, Discovery: "dns", Replicas: []string{"http://shard-a:3000"}}}}
	p := NewPool(m, "token")
	p.Resolver = resolver
	p.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	p.Probe(context.Background())
	if !p.Ready() || len(p.resolved["a"]) != 2 {
		t.Fatalf("targets: %v", p.resolved)
	}
	resolver.hosts = append(resolver.hosts, "10.0.0.3")
	p.Probe(context.Background())
	if len(p.resolved["a"]) != 3 {
		t.Fatal(p.resolved)
	}
	resolver.hosts = []string{"10.0.0.3"}
	p.Probe(context.Background())
	if len(p.healthy) != 1 || !p.healthy["http://10.0.0.3:3000"] {
		t.Fatal(p.healthy)
	}
	resolver.err = errors.New("DNS unavailable")
	p.Probe(context.Background())
	if !p.Ready() || len(p.resolved["a"]) != 1 {
		t.Fatal("DNS failure lost known targets")
	}
	resolver.err = nil
	resolver.hosts = nil
	p.Probe(context.Background())
	if !p.Ready() {
		t.Fatal("empty answer lost known targets")
	}
	fresh := NewPool(m, "token")
	fresh.Resolver = resolver
	fresh.Probe(context.Background())
	if fresh.Ready() {
		t.Fatal("no targets marked ready")
	}
	resolver.hosts = []string{"10.0.0.4"}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				p.Probe(context.Background())
				p.Ready()
				_, _ = p.candidates("a")
			}
		}()
	}
	wg.Wait()
}
func TestDNSIPv6AndDefaultPorts(t *testing.T) {
	p := NewPool(nil, "")
	p.Resolver = &fakeResolver{hosts: []string{"2001:db8::1"}}
	for _, test := range []struct{ raw, want string }{{"http://headless:3000", "http://[2001:db8::1]:3000"}, {"https://headless", "https://[2001:db8::1]"}} {
		got, err := p.resolve(context.Background(), shardmap.Shard{Discovery: "dns", Replicas: []string{test.raw}})
		if err != nil || len(got) != 1 || got[0] != test.want {
			t.Fatalf("%v %v", got, err)
		}
	}
}
