package cluster

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (*FSM, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bank.db")
	f, err := OpenFSM(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f, path
}
func applyOK(t *testing.T, f *FSM, index uint64, c Command) Result {
	t.Helper()
	r, err := f.apply(index, c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func register(t *testing.T, f *FSM) {
	t.Helper()
	r := applyOK(t, f, 1, Command{ID: "signup", Action: "register", Username: "alice", PasswordHash: "hash", Fingerprint: "credentials"})
	if r.HTTP != 201 {
		t.Fatal(r)
	}
}

func TestDurableDeduplicationAndRejection(t *testing.T) {
	f, path := fixture(t)
	register(t, f)
	c := Command{ID: "deposit-1", UserID: 1, Action: "deposit", Account: "A1001", Amount: 100}
	if r := applyOK(t, f, 2, c); r.Balance != 100 {
		t.Fatal(r)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f, err = OpenFSM(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if r := applyOK(t, f, 3, c); r.Balance != 100 {
		t.Fatal(r)
	}
	c.Amount = 200
	if r := applyOK(t, f, 4, c); r.Error != "idempotency_conflict" {
		t.Fatal(r)
	}
	c = Command{ID: "rejected", UserID: 1, Action: "withdraw", Account: "A1001", Amount: 200}
	if r := applyOK(t, f, 5, c); r.Error != "insufficient_funds" {
		t.Fatal(r)
	}
	applyOK(t, f, 6, Command{ID: "deposit-2", UserID: 1, Action: "deposit", Account: "A1001", Amount: 500})
	if r := applyOK(t, f, 7, c); r.Error != "insufficient_funds" {
		t.Fatalf("rejection changed on retry: %+v", r)
	}
	if r := applyOK(t, f, 8, Command{UserID: 1, Action: "balance", Account: "A1001"}); r.Balance != 600 {
		t.Fatal(r)
	}
}

func TestRollbackSnapshotAndReplay(t *testing.T) {
	f, _ := fixture(t)
	register(t, f)
	applyOK(t, f, 2, Command{ID: "fund", UserID: 1, Action: "deposit", Account: "A1001", Amount: 100})
	failed := Command{ID: "fail", UserID: 1, Action: "withdraw", Account: "A1001", Amount: 20, FailBeforeCommit: true}
	if r := applyOK(t, f, 3, failed); r.Error != "injected_failure" {
		t.Fatal(r)
	}
	s, err := f.read()
	if err != nil {
		t.Fatal(err)
	}
	if s.Accounts["A1001"].Balance != 100 || s.Outcomes["1:fail"].HTTP != 500 {
		t.Fatal(s)
	}
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	f2, _ := fixture(t)
	if err = f2.Restore(io.NopCloser(bytes.NewReader(snap.(*snapshot).data))); err != nil {
		t.Fatal(err)
	}
	applyOK(t, f2, 2, Command{ID: "fund", UserID: 1, Action: "deposit", Account: "A1001", Amount: 100}) // replay must not double-apply
	if r := applyOK(t, f2, 4, failed); r.Error != "injected_failure" {
		t.Fatal(r)
	}
	if r := applyOK(t, f2, 5, Command{UserID: 1, Action: "balance", Account: "A1001"}); r.Balance != 100 {
		t.Fatal(r)
	}
}

func TestOwnershipOverflowAndConcurrentOrdering(t *testing.T) {
	f, _ := fixture(t)
	register(t, f)
	if r := applyOK(t, f, 2, Command{ID: "other", UserID: 2, Action: "deposit", Account: "A1001", Amount: 10}); r.Error != "unknown_account" {
		t.Fatal(r)
	}
	applyOK(t, f, 3, Command{ID: "fund", UserID: 1, Action: "deposit", Account: "A1001", Amount: 1000})
	ok := 0
	for i := 0; i < 20; i++ {
		r := applyOK(t, f, uint64(4+i), Command{ID: fmt.Sprint(i), UserID: 1, Action: "withdraw", Account: "A1001", Amount: 100})
		if r.Status == "success" {
			ok++
		} else if r.Error != "insufficient_funds" {
			t.Fatal(r)
		}
	}
	if ok != 10 {
		t.Fatal(ok)
	}
	applyOK(t, f, 24, Command{ID: "max", UserID: 1, Action: "deposit", Account: "A1001", Amount: math.MaxInt64})
	if r := applyOK(t, f, 25, Command{ID: "overflow", UserID: 1, Action: "deposit", Account: "A1001", Amount: 1}); r.Error != "invalid_amount" {
		t.Fatal(r)
	}
}

func TestStorageFailureRollsBackWholeOperation(t *testing.T) {
	f, _ := fixture(t)
	register(t, f)
	// The only durable row update must fail: neither balance nor dedup record
	// may become visible. Production Apply fail-stops on this storage error.
	if err := f.db.Exec(`CREATE TRIGGER fail_write BEFORE UPDATE ON state_rows BEGIN SELECT RAISE(ABORT, 'disk failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.apply(2, Command{ID: "fail", UserID: 1, Action: "deposit", Account: "A1001", Amount: 50}); err == nil {
		t.Fatal("expected persistence failure")
	}
	s, err := f.read()
	if err != nil {
		t.Fatal(err)
	}
	if s.Index != 1 || s.Accounts["A1001"].Balance != 0 || len(s.Outcomes) != 1 {
		t.Fatal(s)
	}
}
