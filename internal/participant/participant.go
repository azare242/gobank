// Package participant implements durable PostgreSQL prepared transactions.
package participant

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/azare242/gobank/internal/shardmap"
)

type Command struct {
	ID          string `json:"id"`
	UserID      uint   `json:"user_id"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Amount      int64  `json:"amount"`
}
type Status struct {
	State string `json:"state"`
}
type Participant struct {
	Crash   func(string)
	Store   *repository.SQLStore
	Map     *shardmap.Map
	ShardID string
}

// TransactionGID is stable across replicas and coordinator/participant restarts.
func TransactionGID(id, shardID string) string {
	return "bank_" + repository.Fingerprint([]string{id, shardID})
}
func (p *Participant) gid(id string) string {
	return TransactionGID(id, p.ShardID)
}
func (p *Participant) Validate(c Command) (string, error) {
	if c.ID == "" || len(c.ID) > 200 || c.UserID == 0 || c.Amount <= 0 || c.Source == c.Destination {
		return "", serviceerr.New(400, "invalid_prepare")
	}
	a, e := p.Map.Owner(c.Source)
	if e != nil {
		return "", e
	}
	b, e := p.Map.Owner(c.Destination)
	if e != nil {
		return "", e
	}
	if a.ID == b.ID {
		return "", serviceerr.New(400, "not_cross_shard")
	}
	if a.ID == p.ShardID {
		return "debit", nil
	}
	if b.ID == p.ShardID {
		return "credit", nil
	}
	return "", serviceerr.New(400, "wrong_shard")
}

// withLock uses a session lock outside the prepared transaction. Every replica
// shares this lock namespace; releasing it never releases prepared account locks.
func (p *Participant) withLock(ctx context.Context, id string, fn func(*sql.Conn) error) error {
	conn, e := p.Store.DB.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	key := "participant:" + id
	for {
		var ok bool
		e = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&ok)
		if e != nil {
			return e
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, e := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key); e != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn(conn)
}
func (p *Participant) state(ctx context.Context, conn *sql.Conn, id string) (string, error) {
	var state string
	e := conn.QueryRowContext(ctx, `SELECT state FROM bank_participants WHERE id=$1`, id).Scan(&state)
	if errors.Is(e, sql.ErrNoRows) {
		return "UNKNOWN", nil
	}
	if e != nil {
		return "", e
	}
	if state != "INIT" {
		return state, nil
	}
	var committed bool
	e = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bank_commits WHERE id=$1)`, id).Scan(&committed)
	if e != nil {
		return "", e
	}
	if committed {
		return "COMMIT", nil
	}
	var prepared bool
	e = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_prepared_xacts WHERE gid=$1 AND database=current_database())`, p.gid(id)).Scan(&prepared)
	if e != nil {
		return "", e
	}
	if prepared {
		return "PREPARED", nil
	}
	return "INIT", nil
}
func (p *Participant) Status(ctx context.Context, id string) (Status, error) {
	var s Status
	e := p.withLock(ctx, id, func(c *sql.Conn) error { var e error; s.State, e = p.state(ctx, c, id); return e })
	return s, e
}
func (p *Participant) Prepare(ctx context.Context, cmd Command) (Status, error) {
	var result Status
	role, e := p.Validate(cmd)
	if e != nil {
		return result, e
	}
	e = p.withLock(ctx, cmd.ID, func(c *sql.Conn) error {
		b, e := json.Marshal(cmd)
		if e != nil {
			return e
		}
		fp := repository.Fingerprint(cmd)
		if _, e = c.ExecContext(ctx, `INSERT INTO bank_participants(id,fingerprint,payload,state) VALUES($1,$2,$3,'INIT') ON CONFLICT DO NOTHING`, cmd.ID, fp, b); e != nil {
			return e
		}
		var old string
		if e = c.QueryRowContext(ctx, `SELECT fingerprint FROM bank_participants WHERE id=$1`, cmd.ID).Scan(&old); e != nil {
			return e
		}
		if old != fp {
			return serviceerr.New(409, "transaction_conflict")
		}
		state, e := p.state(ctx, c, cmd.ID)
		if e != nil {
			return e
		}
		result.State = state
		if state == "COMMIT" || state == "PREPARED" {
			return nil
		}
		if state == "ABORT" {
			return serviceerr.New(409, "already_aborted")
		}
		if _, e = c.ExecContext(ctx, `BEGIN; SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='4s'`); e != nil {
			return e
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = c.ExecContext(cleanup, "ROLLBACK")
		}()
		if _, e = c.ExecContext(ctx, `SELECT set_config('bank.epoch',$1,true)`, repository.Epoch(ctx)); e != nil {
			return e
		}
		account := cmd.Source
		if role == "credit" {
			account = cmd.Destination
		}
		var user uint
		var balance int64
		if e = c.QueryRowContext(ctx, `SELECT user_id,balance FROM bank_accounts WHERE id=$1 FOR UPDATE`, account).Scan(&user, &balance); errors.Is(e, sql.ErrNoRows) {
			return serviceerr.New(404, "unknown_account")
		} else if e != nil {
			return e
		}
		if role == "debit" {
			if user != cmd.UserID {
				return serviceerr.New(404, "unknown_account")
			}
			if balance < cmd.Amount {
				return serviceerr.New(409, "insufficient_funds")
			}
			balance -= cmd.Amount
		} else {
			if balance > math.MaxInt64-cmd.Amount {
				return serviceerr.New(409, "balance_overflow")
			}
			balance += cmd.Amount
		}
		if _, e = c.ExecContext(ctx, `UPDATE bank_accounts SET balance=$1 WHERE id=$2`, balance, account); e != nil {
			return e
		}
		if _, e = c.ExecContext(ctx, `INSERT INTO bank_audits(request_key,user_id,account_id,action,amount) VALUES($1,$2,$3,$4,$5)`, cmd.ID, cmd.UserID, account, role, cmd.Amount); e != nil {
			return e
		}
		if _, e = c.ExecContext(ctx, `INSERT INTO bank_commits(id) VALUES($1)`, cmd.ID); e != nil {
			return e
		}
		// GID consists only of a fixed prefix and hexadecimal hash, never user SQL.
		if _, e = c.ExecContext(ctx, "PREPARE TRANSACTION '"+p.gid(cmd.ID)+"'"); e != nil {
			return e
		}
		if p.Crash != nil {
			p.Crash("after_prepare")
		}
		result.State = "PREPARED"
		return nil
	})
	return result, e
}
func (p *Participant) Decide(ctx context.Context, cmd Command, decision string) (Status, error) {
	var result Status
	if _, e := p.Validate(cmd); e != nil {
		return result, e
	}
	if decision != "COMMIT" && decision != "ABORT" {
		return result, serviceerr.New(400, "invalid_decision")
	}
	e := p.withLock(ctx, cmd.ID, func(c *sql.Conn) error {
		b, _ := json.Marshal(cmd)
		fp := repository.Fingerprint(cmd)
		if _, e := c.ExecContext(ctx, `INSERT INTO bank_participants(id,fingerprint,payload,state) VALUES($1,$2,$3,'INIT') ON CONFLICT DO NOTHING`, cmd.ID, fp, b); e != nil {
			return e
		}
		var old string
		if e := c.QueryRowContext(ctx, `SELECT fingerprint FROM bank_participants WHERE id=$1`, cmd.ID).Scan(&old); e != nil {
			return e
		}
		if old != fp {
			return serviceerr.New(409, "transaction_conflict")
		}
		state, e := p.state(ctx, c, cmd.ID)
		if e != nil {
			return e
		}
		if state == "COMMIT" || state == "ABORT" {
			if state != decision {
				return serviceerr.New(409, "contradictory_decision")
			}
		} else if state == "PREPARED" {
			verb := "COMMIT"
			if decision == "ABORT" {
				verb = "ROLLBACK"
			}
			if _, e = c.ExecContext(ctx, verb+" PREPARED '"+p.gid(cmd.ID)+"'"); e != nil {
				return e
			}
		} else if decision == "COMMIT" {
			return serviceerr.New(409, "not_prepared")
		}
		_, e = c.ExecContext(ctx, `UPDATE bank_participants SET state=$1 WHERE id=$2`, decision, cmd.ID)
		if e == nil && p.Crash != nil {
			p.Crash("after_finalize")
		}
		result.State = decision
		return e
	})
	return result, e
}
func (p *Participant) Ready(ctx context.Context) error {
	var n int
	if e := p.Store.DB.QueryRowContext(ctx, `SHOW max_prepared_transactions`).Scan(&n); e != nil {
		return e
	}
	if n < 1 {
		return fmt.Errorf("max_prepared_transactions must be enabled")
	}
	return nil
}
