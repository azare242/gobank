// Package cluster implements the bonus banking state machine and Raft node.
package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/hashicorp/raft"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Command contains all nondeterministic input, chosen once by the proposer.
type Command struct {
	ID           string `json:"operation_id,omitempty"`
	UserID       uint   `json:"user_id,omitempty"`
	Action       string `json:"action"`
	Account      string `json:"account,omitempty"`
	Amount       int64  `json:"amount,omitempty"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
	// Fingerprint binds registration retries to the original credentials without
	// putting plaintext passwords in the log. It is an HMAC made by the API.
	Fingerprint      string `json:"fingerprint,omitempty"`
	Timestamp        string `json:"timestamp"`
	ClientIP         string `json:"client_ip,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	FailBeforeCommit bool   `json:"fail_before_commit,omitempty"`
	LoginOK          bool   `json:"login_ok,omitempty"`
}

type Result struct {
	HTTP        int    `json:"-"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Message     string `json:"message,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Account     string `json:"account,omitempty"`
	Balance     int64  `json:"balance"`
	UserID      uint   `json:"id,omitempty"`
	Username    string `json:"username,omitempty"`
}

type user struct {
	ID                              uint
	Username, PasswordHash, Account string
}
type account struct {
	Owner   uint
	Balance int64
}
type outcome struct {
	Fingerprint string
	Result      Result
	HTTP        int
}
type audit struct {
	Index   uint64
	Command Command
	Result  Result
	HTTP    int
}
type state struct {
	Index    uint64
	NextUser uint
	Users    map[string]user
	Accounts map[string]account
	Outcomes map[string]outcome
	Audits   []audit
}

func emptyState() state {
	return state{Users: map[string]user{}, Accounts: map[string]account{}, Outcomes: map[string]outcome{}, Audits: []audit{}}
}

// One SQLite row holds the complete small simulated bank. Rewriting it is
// intentionally simple: balances, outcomes, audits, and replay index commit in
// one durable transaction. It is not intended for a large banking workload.
type stateRow struct {
	ID   uint   `gorm:"primaryKey"`
	Data []byte `gorm:"not null"`
}

type FSM struct {
	mu sync.Mutex
	db *gorm.DB
}

func OpenFSM(path string) (*FSM, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sql, err := db.DB()
	if err != nil {
		return nil, err
	}
	sql.SetMaxOpenConns(1)
	f := &FSM{db: db}
	for _, stmt := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000"} {
		if err = db.Exec(stmt).Error; err != nil {
			sql.Close()
			return nil, err
		}
	}
	if err = db.AutoMigrate(&stateRow{}); err != nil {
		sql.Close()
		return nil, err
	}
	b, _ := json.Marshal(emptyState())
	if err = db.Exec("INSERT OR IGNORE INTO state_rows(id, data) VALUES (1, ?)", b).Error; err != nil {
		sql.Close()
		return nil, err
	}
	return f, nil
}

func (f *FSM) Close() error {
	db, err := f.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func load(db *gorm.DB) (state, error) {
	var row stateRow
	if err := db.First(&row, 1).Error; err != nil {
		return state{}, err
	}
	var s state
	err := json.Unmarshal(row.Data, &s)
	if err == nil && (s.Users == nil || s.Accounts == nil || s.Outcomes == nil) {
		err = fmt.Errorf("invalid bank state")
	}
	return s, err
}
func save(db *gorm.DB, s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return db.Model(&stateRow{}).Where("id = 1").Update("data", b).Error
}
func reject(http int, code, message string) Result {
	return Result{HTTP: http, Status: "error", Error: code, Message: message}
}
func operationKey(uid uint, id string) string { return fmt.Sprintf("%d:%s", uid, id) }
func fingerprint(c Command) string {
	// Attempt metadata and bcrypt salt do not change the logical operation.
	b, _ := json.Marshal(struct {
		Action, Account       string
		Amount                int64
		Username, Credentials string
		Fail                  bool
	}{c.Action, c.Account, c.Amount, c.Username, c.Fingerprint, c.FailBeforeCommit})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func execute(s *state, c Command) Result {
	r := Result{HTTP: 200, Status: "success", OperationID: c.ID, Account: c.Account}
	switch c.Action {
	case "register":
		if _, ok := s.Users[c.Username]; ok {
			return reject(409, "conflict", "Username already exists")
		}
		s.NextUser++
		a := fmt.Sprintf("A%d", 1000+s.NextUser)
		u := user{ID: s.NextUser, Username: c.Username, PasswordHash: c.PasswordHash, Account: a}
		s.Users[c.Username] = u
		s.Accounts[a] = account{Owner: u.ID}
		r.HTTP = 201
		r.UserID = u.ID
		r.Username = u.Username
		r.Account = a
	case "deposit", "withdraw", "balance":
		a, ok := s.Accounts[c.Account]
		if !ok || a.Owner != c.UserID {
			return reject(404, "unknown_account", "Account not found")
		}
		if c.Action != "balance" {
			if c.Amount <= 0 {
				return reject(400, "invalid_amount", "Amount must be a positive int64")
			}
			if c.Action == "withdraw" {
				if a.Balance < c.Amount {
					return reject(409, "insufficient_funds", "Account does not have enough balance")
				}
				a.Balance -= c.Amount
			} else {
				if a.Balance > math.MaxInt64-c.Amount {
					return reject(400, "invalid_amount", "Balance overflow")
				}
				a.Balance += c.Amount
			}
			s.Accounts[c.Account] = a
		}
		r.Balance = a.Balance
	case "operation":
		o, ok := s.Outcomes[operationKey(c.UserID, c.ID)]
		if !ok {
			return reject(404, "unknown_operation", "No committed outcome; retry the original operation ID")
		}
		r = o.Result
		r.HTTP = o.HTTP
	case "login":
		if !c.LoginOK {
			return reject(401, "unauthorized", "Invalid username or password")
		}
	case "barrier": // A committed read fence; HTTP reads local state afterwards.
	default:
		return reject(400, "unsupported_action", "Unsupported action")
	}
	return r
}

func (f *FSM) apply(index uint64, c Command) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result Result
	err := f.db.Transaction(func(tx *gorm.DB) error {
		s, err := load(tx)
		if err != nil {
			return err
		}
		key := operationKey(c.UserID, c.ID)
		mutation := c.Action == "deposit" || c.Action == "withdraw" || c.Action == "register"
		if index <= s.Index {
			if o, ok := s.Outcomes[key]; ok {
				result = o.Result
				result.HTTP = o.HTTP
			}
			return nil
		}
		if mutation {
			if o, ok := s.Outcomes[key]; ok {
				result = o.Result
				result.HTTP = o.HTTP
				if o.Fingerprint != fingerprint(c) {
					result = reject(409, "idempotency_conflict", "Operation ID already used for a different payload")
				}
				s.Index = index
				return save(tx, s)
			}
		}
		if c.FailBeforeCommit && (c.Action == "deposit" || c.Action == "withdraw") {
			if err := tx.SavePoint("demo_failure").Error; err != nil {
				return err
			}
			result = execute(&s, c)
			if result.Status == "success" {
				// Actually write the tentative balance to SQLite, then roll it
				// back. Only the failure outcome is committed afterwards.
				if err := save(tx, s); err != nil {
					return err
				}
				if err := tx.RollbackTo("demo_failure").Error; err != nil {
					return err
				}
				s, err = load(tx)
				if err != nil {
					return err
				}
				result = reject(500, "injected_failure", "Tentative SQLite balance write rolled back before commit")
			}
		} else {
			result = execute(&s, c)
		}
		if mutation {
			result.OperationID = c.ID
			s.Outcomes[key] = outcome{Fingerprint: fingerprint(c), Result: result, HTTP: result.HTTP}
			// Credentials never enter audit history; the user table retains hashes.
			c.PasswordHash = ""
			c.Fingerprint = ""
			s.Audits = append(s.Audits, audit{Index: index, Command: c, Result: result, HTTP: result.HTTP})
		}
		if c.Action == "login" {
			s.Audits = append(s.Audits, audit{Index: index, Command: c, Result: result, HTTP: result.HTTP})
		}
		s.Index = index
		return save(tx, s)
	})
	return result, err
}

func (f *FSM) Apply(entry *raft.Log) interface{} {
	var c Command
	if err := json.Unmarshal(entry.Data, &c); err != nil {
		panic(fmt.Sprintf("invalid committed command: %v", err))
	}
	r, err := f.apply(entry.Index, c)
	// Continuing after a storage error would leave this replica inconsistent.
	// A panic here deliberately terminates the process (outside HTTP recovery).
	if err != nil {
		panic(fmt.Sprintf("fatal state machine persistence failure: %v", err))
	}
	return r
}

func (f *FSM) read() (state, error) { f.mu.Lock(); defer f.mu.Unlock(); return load(f.db) }

type snapshot struct{ data []byte }

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	s, err := f.read()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(s)
	return &snapshot{data: b}, err
}
func (s *snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		sink.Cancel()
		return err
	}
	if err := sink.Close(); err != nil {
		sink.Cancel()
		return err
	}
	return nil
}
func (*snapshot) Release() {}
func (f *FSM) Restore(reader io.ReadCloser) error {
	defer reader.Close()
	var s state
	if err := json.NewDecoder(reader).Decode(&s); err != nil {
		return err
	}
	if s.Users == nil || s.Accounts == nil || s.Outcomes == nil {
		return fmt.Errorf("invalid snapshot")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.db.Transaction(func(tx *gorm.DB) error { return save(tx, s) })
}

// Digest is diagnostic and may be stale. It excludes log positions because
// configuration entries/read fences can differ while banking state agrees.
func (f *FSM) Digest() (string, uint64, error) {
	s, err := f.read()
	if err != nil {
		return "", 0, err
	}
	idx := s.Index
	s.Index = 0
	b, err := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), idx, err
}
