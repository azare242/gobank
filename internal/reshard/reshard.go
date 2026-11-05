// Package reshard implements the optional B-to-C online range migration.
// Progress, routing and change records live in PostgreSQL, never process memory.
package reshard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/azare242/gobank/internal/coordinator"
	"github.com/azare242/gobank/internal/shardmap"
)

const RangeLock = "bonus1:A1501-A2000"

// Initialize enables durable routing without changing the configured ownership.
func Initialize(ctx context.Context, db *sql.DB, m *shardmap.Map) error {
	b, e := json.Marshal(m.Shards)
	if e != nil {
		return e
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('bonus1:routing-schema',0))`); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bank_routing(id int PRIMARY KEY CHECK(id=1),epoch bigint NOT NULL,shards jsonb NOT NULL);
 CREATE TABLE IF NOT EXISTS bank_migration(id int PRIMARY KEY CHECK(id=1),phase text NOT NULL,position bigint NOT NULL DEFAULT 0);
 INSERT INTO bank_migration(id,phase) VALUES(1,'NEW') ON CONFLICT DO NOTHING;`)
	if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO bank_routing VALUES(1,1,$1) ON CONFLICT DO NOTHING`, b); e != nil {
		return e
	}
	return tx.Commit()
}

type Worker struct {
	Metadata, Source, Target *sql.DB
	Map                      *shardmap.Map
	Coordinator              *coordinator.Coordinator
	Crash                    func(string)
}

func (w *Worker) hit(s string) {
	if w.Crash != nil {
		w.Crash(s)
	}
}
func (w *Worker) phase(ctx context.Context, s string) error {
	_, e := w.Metadata.ExecContext(ctx, `UPDATE bank_migration SET phase=$1 WHERE id=1`, s)
	return e
}

// Install is idempotent. The guard row is locked by every range mutation,
// including prepared transactions, so fencing cannot overtake a durable prepare.
func install(ctx context.Context, db *sql.DB, active bool, epoch int64, capture bool) error {
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bank_range_guard(id int PRIMARY KEY CHECK(id=1),writable boolean NOT NULL,epoch bigint NOT NULL);
 CREATE TABLE IF NOT EXISTS bank_changes(position bigserial PRIMARY KEY,table_name text NOT NULL, row_data jsonb NOT NULL);
 CREATE OR REPLACE FUNCTION bank_range_fence() RETURNS trigger LANGUAGE plpgsql AS $$
 DECLARE g bank_range_guard; a text;
 BEGIN
 IF current_setting('bank.migration',true)='on' THEN RETURN COALESCE(NEW,OLD); END IF;
 a:=COALESCE(NEW.id,OLD.id);
 IF a >= 'A1501' AND a <= 'A2000' THEN
 SELECT * INTO g FROM bank_range_guard WHERE id=1 FOR SHARE;
 IF NOT g.writable OR COALESCE(NULLIF(current_setting('bank.epoch',true),''),'1')::bigint <> g.epoch THEN
 RAISE EXCEPTION 'stale shard epoch or fenced owner' USING ERRCODE='40001'; END IF;
 END IF;
 RETURN COALESCE(NEW,OLD);
 END $$;
 DROP TRIGGER IF EXISTS bank_fence ON bank_accounts;
 CREATE TRIGGER bank_fence BEFORE INSERT OR UPDATE OR DELETE ON bank_accounts FOR EACH ROW EXECUTE FUNCTION bank_range_fence();`)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO bank_range_guard VALUES(1,$1,$2) ON CONFLICT DO NOTHING`, active, epoch)
	if e != nil {
		return e
	}
	if capture {
		_, e = tx.ExecContext(ctx, `CREATE OR REPLACE FUNCTION bank_capture() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_TABLE_NAME='bank_accounts' THEN
 IF NEW.id < 'A1501' OR NEW.id > 'A2000' THEN RETURN NEW; END IF;
 END IF;
 -- Serializes allocation with commit/prepare order. Prepared locks survive crashes.
 PERFORM pg_advisory_xact_lock(hashtextextended('bonus1:change-order',0));
 INSERT INTO bank_changes(table_name,row_data) VALUES(TG_TABLE_NAME,to_jsonb(NEW)); RETURN NEW;
 END $$;`)
		if e != nil {
			return e
		}
		for _, t := range tables {
			_, e = tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS bank_capture_change ON `+t+`; CREATE TRIGGER bank_capture_change AFTER INSERT OR UPDATE ON `+t+` FOR EACH ROW EXECUTE FUNCTION bank_capture()`)
			if e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}

var tables = []string{"bank_accounts", "bank_operations", "bank_audits", "bank_participants", "bank_commits"}
var keys = map[string]string{"bank_accounts": "id", "bank_operations": "user_id,request_key", "bank_audits": "id", "bank_participants": "id", "bank_commits": "id"}
var updates = map[string]string{"bank_accounts": "user_id=EXCLUDED.user_id,balance=EXCLUDED.balance", "bank_operations": "fingerprint=EXCLUDED.fingerprint,outcome=EXCLUDED.outcome", "bank_audits": "request_key=EXCLUDED.request_key,user_id=EXCLUDED.user_id,account_id=EXCLUDED.account_id,action=EXCLUDED.action,amount=EXCLUDED.amount,created_at=EXCLUDED.created_at", "bank_participants": "fingerprint=EXCLUDED.fingerprint,payload=EXCLUDED.payload,state=EXCLUDED.state", "bank_commits": "id=EXCLUDED.id"}

func apply(ctx context.Context, tx *sql.Tx, table string, b []byte) error {
	key, ok := keys[table]
	if !ok {
		return fmt.Errorf("unknown change table %q", table)
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO `+table+` SELECT * FROM jsonb_populate_record(NULL::`+table+`,$1) ON CONFLICT (`+key+`) DO UPDATE SET `+updates[table], b)
	return e
}
func (w *Worker) snapshot(ctx context.Context) error {
	source, e := w.Source.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return e
	}
	defer source.Rollback()
	target, e := w.Target.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer target.Rollback()
	if _, e = target.ExecContext(ctx, `SET LOCAL bank.migration='on'`); e != nil {
		return e
	}
	for _, t := range tables {
		filter := ""
		if t == "bank_accounts" {
			filter = " WHERE id BETWEEN 'A1501' AND 'A2000'"
		}
		rows, e := source.QueryContext(ctx, `SELECT to_jsonb(t) FROM `+t+` t`+filter)
		if e != nil {
			return e
		}
		for rows.Next() {
			var b []byte
			if e = rows.Scan(&b); e != nil {
				rows.Close()
				return e
			}
			if e = apply(ctx, target, t, b); e != nil {
				rows.Close()
				return e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
	}
	w.hit("snapshot")
	if e = target.Commit(); e != nil {
		return e
	}
	_, e = w.Metadata.ExecContext(ctx, `UPDATE bank_migration SET phase='REPLAY',position=0 WHERE id=1`)
	return e
}
func (w *Worker) replay(ctx context.Context) error {
	var pos int64
	if e := w.Metadata.QueryRowContext(ctx, `SELECT position FROM bank_migration WHERE id=1`).Scan(&pos); e != nil {
		return e
	}
	var end int64
	if e := w.Source.QueryRowContext(ctx, `SELECT COALESCE(max(position),0) FROM bank_changes`).Scan(&end); e != nil {
		return e
	}
	for {
		rows, e := w.Source.QueryContext(ctx, `SELECT position,table_name,row_data FROM bank_changes WHERE position>$1 AND position<=$2 ORDER BY position LIMIT 500`, pos, end)
		if e != nil {
			return e
		}
		type change struct {
			n int64
			t string
			b []byte
		}
		var batch []change
		for rows.Next() {
			var c change
			if e = rows.Scan(&c.n, &c.t, &c.b); e != nil {
				rows.Close()
				return e
			}
			batch = append(batch, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(batch) == 0 {
			return nil
		}
		tx, e := w.Target.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `SET LOCAL bank.migration='on'`)
		for _, c := range batch {
			if e == nil {
				e = apply(ctx, tx, c.t, c.b)
			}
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		w.hit("replay")
		pos = batch[len(batch)-1].n
		if _, e = w.Metadata.ExecContext(ctx, `UPDATE bank_migration SET position=$1 WHERE id=1`, pos); e != nil {
			return e
		}
	}
}

// Step resumes from the durable phase. The metadata session fence also prevents
// two workers from replaying out of order. Call repeatedly after any interruption.
func (w *Worker) Step(ctx context.Context) error {
	conn, e := w.Metadata.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	// Use a transaction lock so cancellation/connection loss always releases it.
	lock, e := conn.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer lock.Rollback()
	var acquired bool
	if e = lock.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('bonus1:worker',0))`).Scan(&acquired); e != nil {
		return e
	}
	if !acquired {
		return nil
	}
	var phase string
	if e = w.Metadata.QueryRowContext(ctx, `SELECT phase FROM bank_migration WHERE id=1`).Scan(&phase); e != nil {
		return e
	}
	if phase == "DONE" {
		return nil
	}
	if phase == "NEW" {
		var occupied bool
		if e = w.Target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bank_accounts)`).Scan(&occupied); e != nil {
			return e
		}
		if occupied {
			return fmt.Errorf("shard C must start with an empty dedicated database")
		}
		if e = w.Coordinator.Recover(ctx); e != nil {
			return e
		}
		if e = install(ctx, w.Target, false, 2, false); e != nil {
			return e
		}
		if e = install(ctx, w.Source, true, 1, true); e != nil {
			return e
		}
		if e = w.phase(ctx, "SNAPSHOT"); e != nil {
			return e
		}
		phase = "SNAPSHOT"
	}
	if phase == "SNAPSHOT" {
		if e = w.snapshot(ctx); e != nil {
			return e
		}
		phase = "REPLAY"
	}
	if phase == "REPLAY" {
		if e = w.replay(ctx); e != nil {
			return e
		}
		// Recovery must run before excluding new range traffic. COMMIT is immutable.
		if e = w.Coordinator.Recover(ctx); e != nil {
			return e
		}
	}
	// Only affected transfers/operations share this lock; other ranges keep running.
	if _, e = lock.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, RangeLock); e != nil {
		return e
	}
	if phase == "REPLAY" {
		var pending bool
		if e = lock.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bank_transactions WHERE status<>'COMPLETED' AND ((payload->>'source') BETWEEN 'A1501' AND 'A2000' OR (payload->>'destination') BETWEEN 'A1501' AND 'A2000'))`).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return fmt.Errorf("range has unresolved transactions; retrying recovery")
		}
		// Persist intent before fencing. A restart never reopens B.
		if e = w.phase(ctx, "FENCING"); e != nil {
			return e
		}
		phase = "FENCING"
	}
	if phase == "FENCING" {
		if _, e = w.Source.ExecContext(ctx, `UPDATE bank_range_guard SET writable=false,epoch=2 WHERE id=1`); e != nil {
			return e
		}
		w.hit("fencing")
		if e = w.replay(ctx); e != nil {
			return e
		}
		// Ensure migrated state equals the fenced source before activation.
		var a, b string
		query := `SELECT md5(COALESCE(string_agg(id||':'||user_id||':'||balance,',' ORDER BY id),'')) FROM bank_accounts WHERE id BETWEEN 'A1501' AND 'A2000'`
		if e = w.Source.QueryRowContext(ctx, query).Scan(&a); e != nil {
			return e
		}
		if e = w.Target.QueryRowContext(ctx, query).Scan(&b); e != nil {
			return e
		}
		if a != b {
			return fmt.Errorf("migration verification failed")
		}
		if e = w.phase(ctx, "ACTIVATING"); e != nil {
			return e
		}
		phase = "ACTIVATING"
	}
	if phase == "ACTIVATING" {
		shards, epoch, e := w.Map.Current()
		if e != nil {
			return e
		}
		if epoch == 1 {
			found := false
			for i := range shards {
				if shards[i].ID == "b" {
					shards[i].Last = 1500
				}
				if shards[i].ID == "c" {
					shards[i].First = 1501
					shards[i].Last = 2000
					found = true
				}
			}
			if !found {
				return fmt.Errorf("dormant shard c required")
			}
			candidate := shardmap.Map{Shards: shards}
			if e = candidate.Validate(); e != nil {
				return e
			}
			b, _ := json.Marshal(shards)
			// Copied audit IDs must never collide with C's first live write.
			if _, e = w.Target.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('bank_audits','id'),GREATEST(COALESCE((SELECT max(id) FROM bank_audits),0),1),true)`); e != nil {
				return e
			}
			// B is fenced before C opens. C cannot be routed until the epoch commits.
			if _, e = w.Target.ExecContext(ctx, `UPDATE bank_range_guard SET writable=true,epoch=2 WHERE id=1`); e != nil {
				return e
			}
			if _, e = w.Metadata.ExecContext(ctx, `UPDATE bank_routing SET shards=$1,epoch=2 WHERE id=1 AND epoch=1`, b); e != nil {
				return e
			}
		}
		w.hit("activation")
		if e = w.phase(ctx, "CLEANUP"); e != nil {
			return e
		}
		phase = "CLEANUP"
	}
	if phase == "CLEANUP" {
		// Routing has moved permanently; stale B requests are rejected by its guard.
		tx, e := w.Source.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `SET LOCAL bank.migration='on'; DELETE FROM bank_accounts WHERE id BETWEEN 'A1501' AND 'A2000'; DELETE FROM bank_audits WHERE account_id BETWEEN 'A1501' AND 'A2000'`); e != nil {
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
		return w.phase(ctx, "DONE")
	}
	return nil
}
func (w *Worker) Run(ctx context.Context) error {
	for {
		if e := w.Step(ctx); e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e
		}
		var phase string
		if e := w.Metadata.QueryRowContext(ctx, `SELECT phase FROM bank_migration WHERE id=1`).Scan(&phase); e != nil {
			return e
		}
		if phase == "DONE" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
