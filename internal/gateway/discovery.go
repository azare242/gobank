package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"sort"

	"github.com/azare242/gobank/internal/observability"
	"github.com/azare242/gobank/internal/shardmap"
)

type Resolver interface {
	LookupHost(context.Context, string) ([]string, error)
}

// DNS uses the system resolver; refresh intervals do not override DNS caching.
func (p *Pool) resolve(ctx context.Context, s shardmap.Shard) ([]string, error) {
	if s.Discovery != "dns" {
		return slices.Clone(s.Replicas), nil
	}
	var targets []string
	for _, raw := range s.Replicas {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, err
		}
		addresses, err := p.Resolver.LookupHost(ctx, u.Hostname())
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("no DNS addresses for shard %s", s.ID)
		}
		for _, address := range addresses {
			if net.ParseIP(address) == nil {
				return nil, fmt.Errorf("invalid DNS address for shard %s", s.ID)
			}
			v := *u
			v.Host = address
			if u.Port() != "" {
				v.Host = net.JoinHostPort(address, u.Port())
			} else if net.ParseIP(address).To4() == nil {
				v.Host = "[" + address + "]"
			}
			targets = append(targets, v.String())
		}
	}
	sort.Strings(targets)
	return slices.Compact(targets), nil
}

func (p *Pool) refresh(ctx context.Context, shards []shardmap.Shard) map[string][]string {
	result := make(map[string][]string)
	for _, s := range shards {
		targets, err := p.resolve(ctx, s)
		p.mu.Lock()
		if err != nil {
			slog.Warn("DNS discovery failed; retaining previous targets", "shard", s.ID)
			targets = slices.Clone(p.resolved[s.ID])
		} else if !slices.Equal(targets, p.resolved[s.ID]) {
			slog.Info("shard targets changed", "shard", s.ID, "targets", targets)
			delete(p.next, s.ID)
		}
		p.mu.Unlock()
		result[s.ID] = targets
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	active := map[string]bool{}
	for _, targets := range result {
		for _, target := range targets {
			active[target] = true
		}
	}
	for target := range p.healthy {
		if !active[target] {
			delete(p.healthy, target)
			for shard := range p.resolved {
				observability.Delete("gobank_shard_replica_healthy", observability.Labels("shard", shard, "replica", target))
			}
		}
	}
	for shard := range p.resolved {
		if _, ok := result[shard]; !ok {
			observability.Delete("gobank_shard_targets", observability.Labels("shard", shard))
		}
	}
	for shard := range p.next {
		if _, ok := result[shard]; !ok {
			delete(p.next, shard)
		}
	}
	for shard, targets := range result {
		observability.Set("gobank_shard_targets", observability.Labels("shard", shard), float64(len(targets)))
	}
	p.resolved = result
	return result
}
