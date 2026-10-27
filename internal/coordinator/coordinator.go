// Package coordinator persists decisions before delivery and recovers them after restart.
package coordinator

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/azare242/gobank/internal/participant"
	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/azare242/gobank/internal/shardmap"
	"github.com/google/uuid"
)

type Transport interface {
	Call(context.Context, string, string, any, any) (string, error)
}
type Coordinator struct {
	DB        *sql.DB
	Map       *shardmap.Map
	Transport Transport
	Crash     func(string)
}
type Member struct {
	ShardID string `json:"shard_id"`
	GID     string `json:"prepared_gid"`
}
type Record struct {
	Participants []Member            `json:"participants"`
	ID           string              `json:"transaction_id"`
	Mutation     repository.Mutation `json:"-"`
	Status       string              `json:"status"`
	Decision     string              `json:"decision"`
	Acks         []string            `json:"acknowledgments"`
	Outcome      repository.Outcome  `json:"outcome"`
	Pending      bool                `json:"pending"`
	Replica      string              `json:"-"`
}

func (c *Coordinator) Migrate(ctx context.Context) error {
	tx, e := c.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = repository.LockKey(ctx, tx, "coordinator-schema"); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bank_transactions(id text PRIMARY KEY,user_id bigint NOT NULL,request_key text NOT NULL,fingerprint text NOT NULL,payload jsonb NOT NULL,status text NOT NULL,decision text NOT NULL DEFAULT '',acks jsonb NOT NULL DEFAULT '[]',outcome jsonb NOT NULL DEFAULT '{}',participants jsonb NOT NULL DEFAULT '[]',updated_at timestamptz NOT NULL DEFAULT now(),UNIQUE(user_id,request_key)); ALTER TABLE bank_transactions ADD COLUMN IF NOT EXISTS participants jsonb NOT NULL DEFAULT '[]';`)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func scan(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var payload, acks, out, members []byte
	e := row.Scan(&r.ID, &payload, &r.Status, &r.Decision, &acks, &out, &members)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(members, &r.Participants); e != nil {
		return r, e
	}
	if e = json.Unmarshal(payload, &r.Mutation); e != nil {
		return r, e
	}
	if e = json.Unmarshal(acks, &r.Acks); e != nil {
		return r, e
	}
	e = json.Unmarshal(out, &r.Outcome)
	if r.Acks == nil {
		r.Acks = []string{}
	}
	r.Pending = r.Status != "COMPLETED"
	return r, e
}

const columns = "id,payload,status,decision,acks,outcome,participants"

func (c *Coordinator) Get(ctx context.Context, id string, user uint) (Record, error) {
	r, e := scan(c.DB.QueryRowContext(ctx, "SELECT "+columns+" FROM bank_transactions WHERE id=$1 AND user_id=$2", id, user))
	if errors.Is(e, sql.ErrNoRows) {
		e = serviceerr.New(404, "unknown_transaction")
	}
	return r, e
}
func (c *Coordinator) save(ctx context.Context, conn *sql.Conn, r *Record) error {
	a, _ := json.Marshal(r.Acks)
	o, _ := json.Marshal(r.Outcome)
	result, e := conn.ExecContext(ctx, `UPDATE bank_transactions SET status=$2,decision=$3,acks=$4,outcome=$5,updated_at=now() WHERE id=$1 AND (decision='' OR decision=$3)`, r.ID, r.Status, r.Decision, a, o)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return serviceerr.New(409, "contradictory_decision")
	}
	r.Pending = r.Status != "COMPLETED"
	return nil
}

// A database session lock fences live tasks, including recovery workers in other
// gateway processes. Crashed sessions release it before recovery chooses ABORT.
func (c *Coordinator) lock(ctx context.Context, conn *sql.Conn, id string) (func(), error) {
	for {
		var ok bool
		e := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "coordinator:"+id).Scan(&ok)
		if e != nil {
			return nil, e
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, e := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "coordinator:"+id); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}, nil
}
func (c *Coordinator) Process(ctx context.Context, m repository.Mutation) (Record, error) {
	var r Record
	if m.UserID == 0 || m.Amount <= 0 || m.Key == "" || len(m.Key) > 200 {
		return r, serviceerr.New(400, "invalid_request")
	}
	if m.Action != "deposit" && m.Action != "withdraw" && m.Action != "transfer" {
		return r, serviceerr.New(400, "unsupported_action")
	}
	if _, e := c.Map.Owner(m.Source); e != nil {
		return r, serviceerr.New(400, "invalid_account")
	}
	if m.Action == "transfer" {
		if _, e := c.Map.Owner(m.Destination); e != nil {
			return r, serviceerr.New(400, "invalid_destination")
		}
		if m.Source == m.Destination {
			return r, serviceerr.New(400, "self_transfer")
		}
	}
	conn, e := c.DB.Conn(ctx)
	if e != nil {
		return r, e
	}
	defer conn.Close()
	rangeUnlock, rangeErr := c.rangeFence(ctx, conn, m)
	if rangeErr != nil {
		return r, rangeErr
	}
	defer rangeUnlock()
	// Lock the user-scoped request identity before inserting: recovery cannot see
	// an INITIATED row and race its creator before that creator acquires its fence.
	identity := fmt.Sprintf("%d:%s", m.UserID, m.Key)
	unlock, e := c.lock(ctx, conn, identity)
	if e != nil {
		return r, e
	}
	defer unlock()
	b, _ := json.Marshal(m)
	id := uuid.NewString()
	a, _ := c.Map.Owner(m.Source)
	shards := []string{a.ID}
	if m.Action == "transfer" {
		b, _ := c.Map.Owner(m.Destination)
		if b.ID != a.ID {
			shards = append(shards, b.ID)
		}
	}
	sort.Strings(shards)
	members := []Member{}
	for _, shard := range shards {
		members = append(members, Member{ShardID: shard, GID: participant.TransactionGID(id, shard)})
	}
	memberJSON, _ := json.Marshal(members)
	res, e := conn.ExecContext(ctx, `INSERT INTO bank_transactions(id,user_id,request_key,fingerprint,payload,status,participants) VALUES($1,$2,$3,$4,$5,'INITIATED',$6) ON CONFLICT(user_id,request_key) DO NOTHING`, id, m.UserID, m.Key, repository.Fingerprint(m), b, memberJSON)
	if e != nil {
		return r, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return r, e
	}
	r, e = scan(conn.QueryRowContext(ctx, "SELECT "+columns+" FROM bank_transactions WHERE user_id=$1 AND request_key=$2", m.UserID, m.Key))
	if e != nil {
		return r, e
	}
	if repository.Fingerprint(m) != repository.Fingerprint(r.Mutation) {
		return r, serviceerr.New(409, "idempotency_conflict")
	}
	e = c.advance(ctx, conn, &r, n == 1)
	return r, e
}
func (c *Coordinator) hit(point string) {
	if c.Crash != nil {
		c.Crash(point)
	}
}
func (c *Coordinator) advance(ctx context.Context, conn *sql.Conn, r *Record, fresh bool) error {
	if r.Status == "COMPLETED" {
		return nil
	}
	m := r.Mutation
	if len(r.Participants) == 0 {
		return fmt.Errorf("transaction has no durable participant list")
	}
	if len(r.Participants) == 1 {
		var out repository.Outcome
		rep, e := c.Transport.Call(ctx, r.Participants[0].ShardID, "/internal/mutate", m, &out)
		if e != nil {
			return e
		}
		r.Replica = rep
		r.Outcome = out
		r.Outcome.TransactionID = r.ID
		r.Status = "COMPLETED"
		r.Decision = "COMMIT"
		if out.Status == "error" {
			r.Decision = "ABORT"
		}
		return c.save(ctx, conn, r)
	}
	shards := []string{}
	for _, member := range r.Participants {
		shards = append(shards, member.ShardID)
	}
	cmd := participant.Command{ID: r.ID, UserID: m.UserID, Source: m.Source, Destination: m.Destination, Amount: m.Amount}
	if r.Decision == "" {
		decision := "ABORT"
		if fresh {
			r.Status = "PREPARING"
			if e := c.save(ctx, conn, r); e != nil {
				return e
			}
			all := true
			for _, shard := range shards {
				var s participant.Status
				_, e := c.Transport.Call(ctx, shard, "/internal/prepare", cmd, &s)
				if e != nil || s.State != "PREPARED" {
					all = false
					break
				}
				c.hit("after_prepare")
			}
			if all {
				r.Status = "PREPARED"
				if e := c.save(ctx, conn, r); e != nil {
					return e
				}
				c.hit("all_prepared")
				decision = "COMMIT"
			}
		}
		r.Decision = decision
		r.Status = decision + "_DECIDED"
		if e := c.save(ctx, conn, r); e != nil {
			return e
		}
		c.hit("after_decision")
	}
	for _, shard := range shards {
		ack := false
		for _, s := range r.Acks {
			ack = ack || s == shard
		}
		if ack {
			continue
		}
		var status participant.Status
		request := struct {
			Command  participant.Command `json:"command"`
			Decision string              `json:"decision"`
		}{cmd, r.Decision}
		_, e := c.Transport.Call(ctx, shard, "/internal/decide", request, &status)
		if e != nil || status.State != r.Decision {
			r.Pending = true
			return nil
		}
		r.Acks = append(r.Acks, shard)
		if e = c.save(ctx, conn, r); e != nil {
			return e
		}
		c.hit("after_ack")
	}
	r.Status = "COMPLETED"
	r.Outcome = repository.Outcome{Status: "success", TransactionID: r.ID, Decision: r.Decision}
	if r.Decision == "ABORT" {
		r.Outcome.Status = "error"
		r.Outcome.Error = "transfer_aborted"
	}
	return c.save(ctx, conn, r)
}
func (c *Coordinator) Recover(ctx context.Context) error {
	rows, e := c.DB.QueryContext(ctx, "SELECT "+columns+" FROM bank_transactions WHERE status<>'COMPLETED' ORDER BY updated_at LIMIT 100")
	if e != nil {
		return e
	}
	var records []Record
	for rows.Next() {
		r, e := scan(rows)
		if e != nil {
			rows.Close()
			return e
		}
		records = append(records, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, record := range records {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		task, cancel := context.WithTimeout(ctx, 20*time.Second)
		e = c.recoverOne(task, record)
		cancel()
		if e != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}
func (c *Coordinator) recoverOne(ctx context.Context, r Record) error {
	conn, e := c.DB.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	rangeUnlock, rangeErr := c.rangeFence(ctx, conn, r.Mutation)
	if rangeErr != nil {
		return rangeErr
	}
	defer rangeUnlock()
	unlock, e := c.lock(ctx, conn, fmt.Sprintf("%d:%s", r.Mutation.UserID, r.Mutation.Key))
	if e != nil {
		return e
	}
	defer unlock()
	r, e = scan(conn.QueryRowContext(ctx, "SELECT "+columns+" FROM bank_transactions WHERE id=$1", r.ID))
	if e != nil {
		return e
	}
	return c.advance(ctx, conn, &r, false)
}
func (c *Coordinator) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = c.Recover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// Range admission shares the migration cutover fence across all gateways.
func (c *Coordinator) rangeFence(ctx context.Context, conn *sql.Conn, m repository.Mutation) (func(), error) {
	noop := func() {}
	affected := func(a string) bool { return a >= "A1501" && a <= "A2000" }
	if c.Map.DB == nil || (!affected(m.Source) && !affected(m.Destination)) {
		return noop, nil
	}
	key := "bonus1:A1501-A2000"
	if _, e := conn.ExecContext(ctx, `SELECT pg_advisory_lock_shared(hashtextextended($1,0))`, key); e != nil {
		return noop, e
	}
	unlock := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, e := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock_shared(hashtextextended($1,0))`, key); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	var phase string
	if e := conn.QueryRowContext(ctx, `SELECT phase FROM bank_migration WHERE id=1`).Scan(&phase); e != nil {
		unlock()
		return noop, e
	}
	if phase == "FENCING" || phase == "ACTIVATING" || phase == "CLEANUP" {
		unlock()
		return noop, serviceerr.New(503, "range_cutover")
	}
	return unlock, nil
}
