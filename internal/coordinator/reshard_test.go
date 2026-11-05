package coordinator_test

import (
	"fmt"
	"github.com/azare242/gobank/internal/participant"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/reshard"
	"github.com/azare242/gobank/internal/shardmap"
	"github.com/azare242/gobank/internal/shardserver"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPostgresLiveReshardRecovery(t *testing.T) {
	for _, crash := range []string{"snapshot", "replay", "fencing", "activation"} {
		t.Run(crash, func(t *testing.T) {
			f := newFixture(t)
			target := testDB(t)
			if e := target.Migrate(f.ctx); e != nil {
				t.Fatal(e)
			}
			cShard := shardmap.Shard{ID: "c"}
			pc := &participant.Participant{Store: target, Map: f.m, ShardID: "c"}
			for i := 0; i < 2; i++ {
				server := httptest.NewServer((&shardserver.Server{Store: target, Map: f.m, ShardID: "c", ReplicaID: fmt.Sprintf("c%d", i), Secret: strings.Repeat("s", 32), Participant: pc}).Handler())
				t.Cleanup(server.Close)
				cShard.Replicas = append(cShard.Replicas, server.URL)
			}
			f.m.Shards = append(f.m.Shards, cShard)
			if e := f.m.Validate(); e != nil {
				t.Fatal(e)
			}
			if e := reshard.Initialize(f.ctx, f.meta.DB, f.m); e != nil {
				t.Fatal(e)
			}
			f.m.DB = f.meta.DB
			f.pool.Probe(f.ctx)
			w := &reshard.Worker{Metadata: f.meta.DB, Source: f.b.DB, Target: target.DB, Map: f.m, Coordinator: f.c}
			// Leave a durable COMMIT with no delivery; migration must finish it first.
			pending := repository.Mutation{UserID: 500, Key: "before-move", Action: "transfer", Source: "A0500", Destination: "A1750", Amount: 13}
			f.c.Crash = func(at string) {
				if at == "after_decision" {
					panic("coordinator crash")
				}
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("missing coordinator crash")
					}
				}()
				_, _ = f.c.Process(f.ctx, pending)
			}()
			f.c.Crash = nil
			fired := false
			writes := func() {
				var wg sync.WaitGroup
				errs := make(chan error, 5)
				for i := 0; i < 5; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						m := repository.Mutation{UserID: 1750, Key: fmt.Sprintf("copy-write-%d", i), Action: "deposit", Source: "A1750", Amount: 2}
						r, e := f.c.Process(f.ctx, m)
						if e == nil && r.Pending {
							e = fmt.Errorf("pending copy write")
						}
						errs <- e
					}(i)
				}
				wg.Wait()
				close(errs)
				for e := range errs {
					if e != nil {
						t.Fatal(e)
					}
				}
				// Exercise PostgreSQL prepares and finalization while change capture is active.
				for i, m := range []repository.Mutation{
					{UserID: 500, Action: "transfer", Source: "A0500", Destination: "A1750", Amount: 7},
					{UserID: 1750, Action: "transfer", Source: "A1750", Destination: "A0500", Amount: 7},
				} {
					m.Key = fmt.Sprintf("copy-cross-%d", i)
					if r, e := f.c.Process(f.ctx, m); e != nil || r.Pending || r.Decision != "COMMIT" {
						t.Fatalf("copy transfer: %+v %v", r, e)
					}
				}

				m := repository.Mutation{UserID: 1750, Key: "local-during-copy", Action: "transfer", Source: "A1750", Destination: "A1250", Amount: 3}
				if _, e := f.c.Process(f.ctx, m); e != nil {
					t.Fatal(e)
				}
			}
			w.Crash = func(at string) {
				if at == "snapshot" {
					writes()
				}
				if at == "fencing" {
					if _, e := f.b.DB.Exec(`UPDATE bank_accounts SET balance=balance+100 WHERE id='A1750'`); e == nil {
						t.Fatal("stale old-owner write accepted")
					}
					// Nonmigrating range is still writable during cutover.
					if _, e := f.c.Process(f.ctx, repository.Mutation{UserID: 1250, Key: "unrelated", Action: "deposit", Source: "A1250", Amount: 1}); e != nil {
						t.Fatal(e)
					}
				}
				if at == crash && !fired {
					fired = true
					panic("migration crash")
				}
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("missing migration crash")
					}
				}()
				if e := w.Step(f.ctx); e != nil {
					t.Fatal(e)
				}
			}()
			// Reconstruct the worker and routing object, discarding process state.
			if e := reshard.Initialize(f.ctx, f.meta.DB, f.m); e != nil {
				t.Fatal(e)
			}
			w = &reshard.Worker{Metadata: f.meta.DB, Source: f.b.DB, Target: target.DB, Map: &shardmap.Map{Shards: f.m.Shards, DB: f.meta.DB}, Coordinator: f.c}
			if e := w.Run(f.ctx); e != nil {
				t.Fatal(e)
			}
			if !fired {
				t.Fatal("crash not reached")
			}
			owner, e := f.m.Owner("A1750")
			if e != nil || owner.ID != "c" {
				t.Fatalf("owner %+v %v", owner, e)
			}
			_, epoch, e := f.m.Current()
			if e != nil || epoch != 2 {
				t.Fatalf("epoch %d %v", epoch, e)
			}
			if got := balance(t, target, "A1750", 1750); got != 1020 {
				t.Fatalf("migrated balance %d", got)
			}
			var n int
			if e = f.b.DB.QueryRow(`SELECT count(*) FROM bank_accounts WHERE id BETWEEN 'A1501' AND 'A2000'`).Scan(&n); e != nil || n != 0 {
				t.Fatalf("old accounts %d %v", n, e)
			}
			if _, e = f.b.DB.Exec(`INSERT INTO bank_accounts VALUES('A1750',1750,999)`); e == nil {
				t.Fatal("stale owner reinsert accepted")
			}
			if _, e = target.DB.Exec(`UPDATE bank_accounts SET balance=999 WHERE id='A1750'`); e == nil {
				t.Fatal("stale epoch accepted on C")
			}
			// Retry a pre-migration identity; then execute a new cross-shard transfer.
			if r, e := f.c.Process(f.ctx, pending); e != nil || r.Pending || r.Decision != "COMMIT" {
				t.Fatalf("retry %+v %v", r, e)
			}
			m := repository.Mutation{UserID: 1750, Key: "after-move", Action: "transfer", Source: "A1750", Destination: "A0500", Amount: 20}
			if r, e := f.c.Process(f.ctx, m); e != nil || r.Pending || r.Decision != "COMMIT" {
				t.Fatalf("new transfer %+v %v", r, e)
			}
			if got := balance(t, target, "A1750", 1750); got != 1000 {
				t.Fatalf("final balance %d", got)
			}
			for _, db := range []*repository.SQLStore{f.a, f.b, target} {
				if n := prepared(t, db.DB); n != 0 {
					t.Fatalf("orphan prepares %d", n)
				}
			}
			if e := w.Run(f.ctx); e != nil {
				t.Fatal(e)
			}
		})
	}
}
