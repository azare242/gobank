package coordinator_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/bankclient"
	"github.com/azare242/gobank/internal/coordinator"
	"github.com/azare242/gobank/internal/gateway"
	"github.com/azare242/gobank/internal/participant"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/service"
	"github.com/azare242/gobank/internal/shardmap"
	"github.com/azare242/gobank/internal/shardserver"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type fixture struct {
	ctx        context.Context
	m          *shardmap.Map
	a, b, meta *repository.SQLStore
	pa, pb     *participant.Participant
	c          *coordinator.Coordinator
	pool       *gateway.Pool
}

func testDB(t *testing.T) *repository.SQLStore {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TEST_POSTGRES_DSN to a dedicated PostgreSQL with max_prepared_transactions enabled")
	}
	ctx := context.Background()
	admin, e := repository.OpenSQL(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatal("Assignment 2 integration DSN must be a postgres URL")
	}
	name := "bank_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.DB.ExecContext(ctx, "CREATE DATABASE "+name); e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	store, e := repository.OpenSQL(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		rows, e := store.DB.Query(`SELECT gid FROM pg_prepared_xacts WHERE database=current_database()`)
		if e == nil {
			var gids []string
			for rows.Next() {
				var gid string
				_ = rows.Scan(&gid)
				gids = append(gids, gid)
			}
			rows.Close()
			for _, gid := range gids {
				_, _ = store.DB.Exec("ROLLBACK PREPARED '" + gid + "'")
			}
		}
		store.DB.Close()
		_, _ = admin.DB.Exec("DROP DATABASE " + name + " WITH (FORCE)")
		admin.DB.Close()
	})
	return store
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{ctx: context.Background(), a: testDB(t), b: testDB(t), meta: testDB(t)}
	f.m = &shardmap.Map{Shards: []shardmap.Shard{{ID: "a", First: 1, Last: 1000}, {ID: "b", First: 1001, Last: 2000}}}
	for _, s := range []*repository.SQLStore{f.a, f.b} {
		if e := s.Migrate(f.ctx); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.a.Seed(f.ctx, 1, 1000); e != nil {
		t.Fatal(e)
	}
	if e := f.b.Seed(f.ctx, 1001, 2000); e != nil {
		t.Fatal(e)
	}
	f.pa = &participant.Participant{Store: f.a, Map: f.m, ShardID: "a"}
	f.pb = &participant.Participant{Store: f.b, Map: f.m, ShardID: "b"}
	if e := f.pa.Ready(f.ctx); e != nil {
		t.Fatal(e)
	}
	for i, p := range []*participant.Participant{f.pa, f.pb} {
		for r := 0; r < 2; r++ {
			s := &shardserver.Server{Store: p.Store, Map: f.m, ShardID: p.ShardID, ReplicaID: fmt.Sprintf("%s%d", p.ShardID, r+1), Secret: strings.Repeat("s", 32), Participant: p}
			server := httptest.NewServer(s.Handler())
			t.Cleanup(server.Close)
			f.m.Shards[i].Replicas = append(f.m.Shards[i].Replicas, server.URL)
		}
	}
	if e := f.m.Validate(); e != nil {
		t.Fatal(e)
	}
	f.pool = gateway.NewPool(f.m, strings.Repeat("s", 32))
	f.pool.Probe(f.ctx)
	f.c = &coordinator.Coordinator{DB: f.meta.DB, Map: f.m, Transport: f.pool}
	if e := f.c.Migrate(f.ctx); e != nil {
		t.Fatal(e)
	}
	return f
}
func balance(t *testing.T, s *repository.SQLStore, id string, user uint) int64 {
	t.Helper()
	o, e := s.Balance(context.Background(), user, id)
	if e != nil {
		t.Fatal(e)
	}
	return o.Balance
}
func prepared(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM pg_prepared_xacts WHERE database=current_database()`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func mutation(key string) repository.Mutation {
	return repository.Mutation{UserID: 500, Key: key, Action: "transfer", Source: "A0500", Destination: "A1500", Amount: 300}
}
func TestPostgresLocalAtomicityAndDuplicates(t *testing.T) {
	f := newFixture(t)
	m := repository.Mutation{UserID: 500, Key: "duplicate", Action: "deposit", Source: "A0500", Amount: 7}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out repository.Outcome
			_, e := f.pool.Call(f.ctx, "a", "/internal/mutate", m, &out)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if got := balance(t, f.a, "A0500", 500); got != 1007 {
		t.Fatal(got)
	}
	m.Amount = 8
	if _, e := service.NewSQLBank(f.a).Mutate(f.ctx, m); e == nil {
		t.Fatal("payload conflict accepted")
	}
	m = repository.Mutation{UserID: 500, Key: "local", Action: "transfer", Source: "A0500", Destination: "A0501", Amount: 300}
	out, e := service.NewSQLBank(f.a).Mutate(f.ctx, m)
	if e != nil || out.Status != "success" {
		t.Fatalf("%+v %v", out, e)
	}
	if balance(t, f.a, "A0501", 501) != 1300 {
		t.Fatal("missing credit")
	}
	// A failing audit must roll back both balances and deduplication state.
	_, e = f.a.DB.Exec(`CREATE FUNCTION reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_audit BEFORE INSERT ON bank_audits FOR EACH ROW EXECUTE FUNCTION reject_audit()`)
	if e != nil {
		t.Fatal(e)
	}
	m.Key = "audit-failure"
	before := balance(t, f.a, "A0500", 500)
	if _, e = service.NewSQLBank(f.a).Mutate(f.ctx, m); e == nil {
		t.Fatal("audit failure accepted")
	}
	if balance(t, f.a, "A0500", 500) != before || balance(t, f.a, "A0501", 501) != 1300 {
		t.Fatal("partial local transfer")
	}
	_, _ = f.a.DB.Exec(`DROP TRIGGER reject_audit ON bank_audits`)
	_, _ = f.a.DB.Exec(`UPDATE bank_accounts SET balance=$1 WHERE id='A0501'`, int64(math.MaxInt64))
	m.Key = "overflow"
	out, e = service.NewSQLBank(f.a).Mutate(f.ctx, m)
	if e != nil || out.Error != "balance_overflow" {
		t.Fatalf("%+v %v", out, e)
	}
	if balance(t, f.a, "A0500", 500) != before {
		t.Fatal("overflow debited source")
	}
	m.Key = "owner"
	m.UserID = 501
	out, e = service.NewSQLBank(f.a).Mutate(f.ctx, m)
	if e != nil || out.Error != "unknown_account" {
		t.Fatalf("%+v %v", out, e)
	}
}
func TestPostgresPreparedIdempotenceAndDecisions(t *testing.T) {
	f := newFixture(t)
	cmd := participant.Command{ID: uuid.NewString(), UserID: 500, Source: "A0500", Destination: "A1500", Amount: 300}
	for i := 0; i < 2; i++ {
		s, e := f.pa.Prepare(f.ctx, cmd)
		if e != nil || s.State != "PREPARED" {
			t.Fatalf("%+v %v", s, e)
		}
	}
	if balance(t, f.a, "A0500", 500) != 1000 || prepared(t, f.a.DB) != 1 {
		t.Fatal("tentative debit visible or not prepared")
	}
	conflict := cmd
	conflict.Amount++
	if _, e := f.pa.Prepare(f.ctx, conflict); e == nil {
		t.Fatal("prepare conflict accepted")
	}
	for i := 0; i < 2; i++ {
		if _, e := f.pa.Decide(f.ctx, cmd, "COMMIT"); e != nil {
			t.Fatal(e)
		}
	}
	if balance(t, f.a, "A0500", 500) != 700 || prepared(t, f.a.DB) != 0 {
		t.Fatal("commit duplicated")
	}
	if _, e := f.pa.Decide(f.ctx, cmd, "ABORT"); e == nil {
		t.Fatal("contradictory abort accepted")
	}
	// Simulate a process dying between COMMIT PREPARED and metadata update.
	cmd.ID = uuid.NewString()
	if _, e := f.pb.Prepare(f.ctx, cmd); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.DB.Exec("COMMIT PREPARED '" + participant.TransactionGID(cmd.ID, "b") + "'"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pb.Decide(f.ctx, cmd, "COMMIT"); e != nil {
		t.Fatal(e)
	}
	if balance(t, f.b, "A1500", 1500) != 800 {
		t.Fatal("commit marker reconciliation duplicated credit")
	}
	// An absent GID without the atomic marker must never count as COMMIT.
	cmd.ID = uuid.NewString()
	if _, e := f.pb.Prepare(f.ctx, cmd); e != nil {
		t.Fatal(e)
	}
	if _, e := f.b.DB.Exec("ROLLBACK PREPARED '" + participant.TransactionGID(cmd.ID, "b") + "'"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pb.Decide(f.ctx, cmd, "COMMIT"); e == nil {
		t.Fatal("missing prepared GID was interpreted as commit")
	}
	if _, e := f.pb.Decide(f.ctx, cmd, "ABORT"); e != nil {
		t.Fatal(e)
	}
	cmd.ID = uuid.NewString()
	if _, e := f.pb.Decide(f.ctx, cmd, "ABORT"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.pb.Prepare(f.ctx, cmd); e == nil {
		t.Fatal("abort tombstone ignored")
	}
}
func TestPostgresCrashRecovery(t *testing.T) {
	f := newFixture(t)
	for _, point := range []string{"after_prepare", "all_prepared", "after_decision", "after_ack"} {
		t.Run(point, func(t *testing.T) {
			_, _ = f.a.DB.Exec(`UPDATE bank_accounts SET balance=1000 WHERE id='A0500'`)
			_, _ = f.b.DB.Exec(`UPDATE bank_accounts SET balance=500 WHERE id='A1500'`)
			f.c.Crash = func(p string) {
				if p == point {
					panic("crash")
				}
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("crash point not hit")
					}
				}()
				_, _ = f.c.Process(f.ctx, mutation(point))
			}()
			f.c.Crash = nil
			restarted := &coordinator.Coordinator{DB: f.meta.DB, Map: f.m, Transport: f.pool}
			if e := restarted.Recover(f.ctx); e != nil {
				t.Fatal(e)
			}
			r, e := restarted.Process(f.ctx, mutation(point))
			if e != nil || r.Pending {
				t.Fatalf("%+v %v", r, e)
			}
			wantA, wantB := int64(1000), int64(500)
			decision := "ABORT"
			if point == "after_decision" || point == "after_ack" {
				wantA, wantB, decision = 700, 800, "COMMIT"
			}
			if r.Decision != decision || balance(t, f.a, "A0500", 500) != wantA || balance(t, f.b, "A1500", 1500) != wantB {
				t.Fatalf("wrong recovery: %+v", r)
			}
			if prepared(t, f.a.DB)+prepared(t, f.b.DB) != 0 {
				t.Fatal("orphan prepared transaction")
			}
		})
	}
	// Destination rejects overflow after the source voted YES: both remain unchanged.
	_, _ = f.a.DB.Exec(`UPDATE bank_accounts SET balance=1000 WHERE id='A0500'`)
	_, _ = f.b.DB.Exec(`UPDATE bank_accounts SET balance=$1 WHERE id='A1500'`, int64(math.MaxInt64))
	r, e := f.c.Process(f.ctx, mutation("no-vote"))
	if e != nil || r.Decision != "ABORT" || r.Pending || balance(t, f.a, "A0500", 500) != 1000 {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestPostgresGatewayTransfersAndConservation(t *testing.T) {
	f := newFixture(t)
	tokens, e := auth.New(auth.Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "test", Audience: "test"})
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("fixture-password"), bcrypt.MinCost)
	server := httptest.NewServer((&gateway.Server{Pool: f.pool, Coordinator: f.c, Tokens: tokens, FixturePasswordHash: hash}).Handler())
	defer server.Close()
	token, _ := tokens.Generate(500)
	client, _ := bankclient.New(server.URL, token)
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"source": "A0500", "destination": "A1500", "amount": 300})
	reply, e := client.Do(ctx, "POST", "/transfers", "example", body)
	if e != nil || reply.Status != "success" {
		t.Fatalf("%+v %v", reply, e)
	}
	if balance(t, f.a, "A0500", 500) != 700 || balance(t, f.b, "A1500", 1500) != 800 {
		t.Fatal("PDF example failed")
	}
	destinationToken, _ := tokens.Generate(1500)
	total, e := bankclient.VerifyBalances(ctx, server.URL, map[string]string{"A0500": token, "A1500": destinationToken}, map[string]int64{"A0500": 700, "A1500": 800})
	if e != nil || total.Int64() != 1500 {
		t.Fatalf("multi-account verification: %v %v", total, e)
	}
	// Opposing and overlapping transfers use a deterministic global shard order.
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m := mutation(fmt.Sprintf("concurrent-%d", i))
			m.Amount = 1
			if i%2 == 1 {
				m.UserID = 1500
				m.Source, m.Destination = m.Destination, m.Source
			}
			_, e := f.c.Process(ctx, m)
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
	if e := f.c.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if balance(t, f.a, "A0500", 500)+balance(t, f.b, "A1500", 1500) != 1500 {
		t.Fatal("money not conserved")
	}
	if prepared(t, f.a.DB)+prepared(t, f.b.DB) != 0 {
		t.Fatal("orphan prepares")
	}
	// Global request identity detects reuse across shard destinations and actions.
	m := mutation("example")
	m.Destination = "A0501"
	if _, e := f.c.Process(ctx, m); e == nil {
		t.Fatal("global key conflict accepted")
	}
}

type unavailable struct{}

func (unavailable) Call(context.Context, string, string, any, any) (string, error) {
	return "", fmt.Errorf("offline")
}

type lostReply struct {
	delegate coordinator.Transport
	lost     bool
}

func (l *lostReply) Call(ctx context.Context, shard, path string, input, output any) (string, error) {
	rep, e := l.delegate.Call(ctx, shard, path, input, output)
	if path == "/internal/decide" && !l.lost && e == nil {
		l.lost = true
		return "", fmt.Errorf("reply lost after finalization")
	}
	return rep, e
}
func TestPostgresUnavailableRecoveryAndLostReplies(t *testing.T) {
	f := newFixture(t)
	// Crash with a durable COMMIT; no participant availability may change it.
	f.c.Crash = func(point string) {
		if point == "after_decision" {
			panic("crash")
		}
	}
	func() { defer func() { _ = recover() }(); _, _ = f.c.Process(f.ctx, mutation("offline")) }()
	f.c.Crash = nil
	restarted := &coordinator.Coordinator{DB: f.meta.DB, Map: f.m, Transport: unavailable{}}
	if e := restarted.Recover(f.ctx); e != nil {
		t.Fatal(e)
	}
	var id, decision, status string
	if e := f.meta.DB.QueryRow(`SELECT id,decision,status FROM bank_transactions WHERE request_key='offline'`).Scan(&id, &decision, &status); e != nil {
		t.Fatal(e)
	}
	if decision != "COMMIT" || status == "COMPLETED" {
		t.Fatal("offline recovery changed decision")
	}
	// Lost finalization reply leaves no ack, but another replica reconciles the
	// commit marker and returns the same decision without applying money twice.
	restarted.Transport = &lostReply{delegate: f.pool}
	if e := restarted.Recover(f.ctx); e != nil {
		t.Fatal(e)
	}
	restarted.Transport = f.pool
	if e := restarted.Recover(f.ctx); e != nil {
		t.Fatal(e)
	}
	r, e := restarted.Get(f.ctx, id, 500)
	if e != nil || r.Pending || r.Decision != "COMMIT" {
		t.Fatalf("%+v %v", r, e)
	}
	if _, e = restarted.Get(f.ctx, id, 501); e == nil {
		t.Fatal("foreign transaction visible")
	}
	if balance(t, f.a, "A0500", 500) != 700 || balance(t, f.b, "A1500", 1500) != 800 {
		t.Fatal("lost reply duplicated effect")
	}
	if prepared(t, f.a.DB)+prepared(t, f.b.DB) != 0 {
		t.Fatal("orphan prepares")
	}
}
