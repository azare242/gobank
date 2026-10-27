// Package shardmap validates ownership and reads optional durable routing epochs.
package shardmap

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Shard struct {
	ID       string   `json:"id"`
	First    int      `json:"first"`
	Last     int      `json:"last"`
	Replicas []string `json:"replicas"`
}
type Map struct {
	Shards []Shard `json:"shards"`
	DB     *sql.DB `json:"-"`
}

func Number(account string) (int, error) {
	if len(account) != 5 || account[0] != 'A' {
		return 0, fmt.Errorf("invalid account ID")
	}
	for _, c := range account[1:] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid account ID")
		}
	}
	n, err := strconv.Atoi(account[1:])
	if err != nil || n < 1 || n > 2000 {
		return 0, fmt.Errorf("account outside supported range")
	}
	return n, nil
}
func Load(path string) (*Map, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Map
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, m.Validate()
}
func (m *Map) Validate() error {
	if len(m.Shards) < 2 {
		return fmt.Errorf("at least two shards are required")
	}
	covered := make([]bool, 2001)
	ids := map[string]bool{}
	urls := map[string]bool{}
	for _, s := range m.Shards {
		if s.ID == "" || ids[s.ID] || (s.First < 1 && (s.First != 0 || s.Last != 0)) || s.Last > 2000 || s.First > s.Last || len(s.Replicas) < 2 {
			return fmt.Errorf("invalid shard configuration: %s", s.ID)
		}
		ids[s.ID] = true
		for n := s.First; n >= 1 && n <= s.Last; n++ {
			if covered[n] {
				return fmt.Errorf("overlapping account ranges")
			}
			covered[n] = true
		}
		for _, raw := range s.Replicas {
			u, e := url.Parse(raw)
			if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || urls[raw] {
				return fmt.Errorf("invalid or duplicate replica URL %q", raw)
			}
			urls[raw] = true
		}
	}
	for n := 1; n <= 2000; n++ {
		if !covered[n] {
			return fmt.Errorf("missing account range at %d", n)
		}
	}
	return nil
}
func (m *Map) Owner(account string) (Shard, error) {
	n, e := Number(account)
	if e != nil {
		return Shard{}, e
	}
	shards, _, e := m.Current()
	if e != nil {
		return Shard{}, e
	}
	for _, s := range shards {
		if n >= s.First && n <= s.Last {
			return s, nil
		}
	}
	return Shard{}, fmt.Errorf("unmapped account")
}
func (m *Map) ByID(id string) (Shard, error) {
	shards, _, e := m.Current()
	if e != nil {
		return Shard{}, e
	}
	for _, s := range shards {
		if s.ID == id {
			return s, nil
		}
	}
	return Shard{}, fmt.Errorf("unknown shard %q", id)
}

// Current reads the authoritative durable epoch. A metadata outage fails closed.
func (m *Map) Current() ([]Shard, int64, error) {
	if m.DB == nil {
		return m.Shards, 1, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var b []byte
	var epoch int64
	e := m.DB.QueryRowContext(ctx, "SELECT shards,epoch FROM bank_routing WHERE id=1").Scan(&b, &epoch)
	if e != nil {
		return nil, 0, e
	}
	var shards []Shard
	e = json.Unmarshal(b, &shards)
	return shards, epoch, e
}
