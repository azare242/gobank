package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/azare242/gobank/internal/serviceerr"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// SQLStore is the PostgreSQL-only Assignment 2 repository. Legacy stores are unchanged.
type SQLStore struct{ DB *sql.DB }
type Mutation struct {
	UserID      uint   `json:"user_id"`
	Key         string `json:"key"`
	Action      string `json:"action"`
	Source      string `json:"source"`
	Destination string `json:"destination,omitempty"`
	Amount      int64  `json:"amount"`
}
type Outcome struct {
	Status        string `json:"status"`
	Account       string `json:"account,omitempty"`
	Balance       int64  `json:"balance"`
	TransactionID string `json:"transaction_id,omitempty"`
	Decision      string `json:"decision,omitempty"`
	Pending       bool   `json:"pending"`
	Error         string `json:"error,omitempty"`
}

func Fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func OpenSQL(ctx context.Context, dsn string) (*SQLStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("PostgreSQL DSN is required")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(5)
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return &SQLStore{db}, nil
}
func (s *SQLStore) Migrate(ctx context.Context) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = LockKey(ctx, tx, "bank-schema"); e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bank_accounts(id text PRIMARY KEY, user_id bigint NOT NULL UNIQUE, balance bigint NOT NULL CHECK(balance>=0));
 CREATE TABLE IF NOT EXISTS bank_operations(user_id bigint NOT NULL, request_key text NOT NULL, fingerprint text NOT NULL, outcome jsonb NOT NULL, PRIMARY KEY(user_id,request_key));
 CREATE TABLE IF NOT EXISTS bank_audits(id bigserial PRIMARY KEY, request_key text NOT NULL, user_id bigint NOT NULL, account_id text NOT NULL, action text NOT NULL, amount bigint NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS bank_participants(id text PRIMARY KEY, fingerprint text NOT NULL, payload jsonb NOT NULL, state text NOT NULL CHECK(state IN ('INIT','COMMIT','ABORT')));
 CREATE TABLE IF NOT EXISTS bank_commits(id text PRIMARY KEY);`)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (s *SQLStore) Seed(ctx context.Context, first, last int) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO bank_accounts(id,user_id,balance) SELECT 'A'||lpad(n::text,4,'0'),n,CASE WHEN n=1500 THEN 500 ELSE 1000 END FROM generate_series($1::int,$2::int) n ON CONFLICT DO NOTHING`, first, last)
	return e
}
func (s *SQLStore) Balance(ctx context.Context, user uint, id string) (Outcome, error) {
	o := Outcome{Status: "success", Account: id}
	e := s.DB.QueryRowContext(ctx, `SELECT balance FROM bank_accounts WHERE id=$1 AND user_id=$2`, id, user).Scan(&o.Balance)
	if errors.Is(e, sql.ErrNoRows) {
		e = serviceerr.New(404, "unknown_account")
	}
	return o, e
}
func ConfigureTx(ctx context.Context, tx *sql.Tx) error {
	_, e := tx.ExecContext(ctx, `SET LOCAL lock_timeout='1500ms'; SET LOCAL statement_timeout='4s'`)
	return e
}
func LockKey(ctx context.Context, tx *sql.Tx, key string) error {
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key)
	return e
}
func (s *SQLStore) Retry(ctx context.Context, fn func(*sql.Tx) error) error {
	for i := 0; i < 3; i++ {
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		e = ConfigureTx(ctx, tx)
		if e == nil {
			e = fn(tx)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if e == nil {
			return nil
		}
		var pg *pgconn.PgError
		if !errors.As(e, &pg) || (pg.Code != "40001" && pg.Code != "40P01" && pg.Code != "55P03") {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 30 * time.Millisecond):
		}
	}
	return serviceerr.New(503, "contention")
}
