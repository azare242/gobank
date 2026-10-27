package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/azare242/gobank/internal/repository"
	"github.com/azare242/gobank/internal/serviceerr"
)

// SQLBank applies Assignment 2 banking rules inside the repository transaction.
type SQLBank struct{ Store *repository.SQLStore }

func NewSQLBank(store *repository.SQLStore) *SQLBank { return &SQLBank{Store: store} }

func (s *SQLBank) Mutate(ctx context.Context, m repository.Mutation) (repository.Outcome, error) {
	var out repository.Outcome
	if m.Amount <= 0 || m.Key == "" || len(m.Key) > 200 || m.UserID == 0 {
		return out, serviceerr.New(400, "invalid_request")
	}
	if m.Action != "deposit" && m.Action != "withdraw" && m.Action != "transfer" {
		return out, serviceerr.New(400, "unsupported_action")
	}
	if m.Action == "transfer" && m.Source == m.Destination {
		return out, serviceerr.New(400, "self_transfer")
	}
	e := s.Store.Retry(ctx, func(tx *sql.Tx) error {
		if e := repository.LockKey(ctx, tx, fmt.Sprintf("operation:%d:%s", m.UserID, m.Key)); e != nil {
			return e
		}
		var fp string
		var b []byte
		e := tx.QueryRowContext(ctx, `SELECT fingerprint,outcome FROM bank_operations WHERE user_id=$1 AND request_key=$2`, m.UserID, m.Key).Scan(&fp, &b)
		if e == nil {
			if fp != repository.Fingerprint(m) {
				return serviceerr.New(409, "idempotency_conflict")
			}
			return json.Unmarshal(b, &out)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		// Validation failures are durable terminal outcomes; database failures roll back.
		if _, e = tx.ExecContext(ctx, "SAVEPOINT mutation"); e != nil {
			return e
		}
		out, e = Apply(ctx, tx, m)
		if e != nil {
			var app *serviceerr.Error
			if !errors.As(e, &app) {
				return e
			}
			if _, e = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT mutation"); e != nil {
				return e
			}
			out = repository.Outcome{Status: "error", Error: app.Code}
		}
		b, e = json.Marshal(out)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO bank_operations VALUES($1,$2,$3,$4)`, m.UserID, m.Key, repository.Fingerprint(m), b)
		return e
	})
	return out, e
}
func Apply(ctx context.Context, tx *sql.Tx, m repository.Mutation) (repository.Outcome, error) {
	out := repository.Outcome{Status: "success", Account: m.Source}
	rows, e := tx.QueryContext(ctx, `SELECT id,user_id,balance FROM bank_accounts WHERE id=$1 OR id=$2 ORDER BY id FOR UPDATE`, m.Source, m.Destination)
	if e != nil {
		return out, e
	}
	type account struct {
		user    uint
		balance int64
	}
	accounts := map[string]account{}
	for rows.Next() {
		var id string
		var a account
		if e = rows.Scan(&id, &a.user, &a.balance); e != nil {
			rows.Close()
			return out, e
		}
		accounts[id] = a
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	a, ok := accounts[m.Source]
	if !ok || a.user != m.UserID {
		return out, serviceerr.New(404, "unknown_account")
	}
	if m.Action == "deposit" {
		if a.balance > math.MaxInt64-m.Amount {
			return out, serviceerr.New(409, "balance_overflow")
		}
		a.balance += m.Amount
	} else {
		if a.balance < m.Amount {
			return out, serviceerr.New(409, "insufficient_funds")
		}
		a.balance -= m.Amount
	}
	if m.Action == "transfer" {
		d, ok := accounts[m.Destination]
		if !ok {
			return out, serviceerr.New(404, "unknown_destination")
		}
		if d.balance > math.MaxInt64-m.Amount {
			return out, serviceerr.New(409, "balance_overflow")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE bank_accounts SET balance=balance+$1 WHERE id=$2`, m.Amount, m.Destination); e != nil {
			return out, e
		}
		if e = auditSQL(ctx, tx, m, m.Destination, "credit"); e != nil {
			return out, e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE bank_accounts SET balance=$1 WHERE id=$2`, a.balance, m.Source); e != nil {
		return out, e
	}
	if e = auditSQL(ctx, tx, m, m.Source, m.Action); e != nil {
		return out, e
	}
	out.Balance = a.balance
	return out, nil
}
func auditSQL(ctx context.Context, tx *sql.Tx, m repository.Mutation, id, action string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO bank_audits(request_key,user_id,account_id,action,amount) VALUES($1,$2,$3,$4,$5)`, m.Key, m.UserID, id, action, m.Amount)
	return e
}
