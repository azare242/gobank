package cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/hashicorp/raft"
	bolt "github.com/hashicorp/raft-boltdb/v2"
)

type testCluster struct {
	nodes [3]*Node
	trans [3]*raft.InmemTransport
	dirs  [3]string
	peers []Peer
}

func newTestCluster(t *testing.T) *testCluster {
	t.Helper()
	c := &testCluster{}
	for i := 0; i < 3; i++ {
		id := fmt.Sprint(i)
		c.peers = append(c.peers, Peer{ID: id, Raft: id, HTTP: "http://" + id})
		c.dirs[i] = t.TempDir()
	}
	for i := 0; i < 3; i++ {
		c.start(t, i, i == 0)
	}
	c.connect()
	t.Cleanup(func() {
		for _, n := range c.nodes {
			if n != nil {
				n.Close()
			}
		}
		for _, tr := range c.trans {
			if tr != nil {
				tr.Close()
			}
		}
	})
	c.leader(t, -1)
	return c
}
func (c *testCluster) start(t *testing.T, i int, bootstrap bool) {
	t.Helper()
	f, err := OpenFSM(filepath.Join(c.dirs[i], "bank.db"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := bolt.NewBoltStore(filepath.Join(c.dirs[i], "raft.db"))
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := raft.NewFileSnapshotStore(c.dirs[i], 2, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	_, tr := raft.NewInmemTransport(raft.ServerAddress(fmt.Sprint(i)))
	c.trans[i] = tr
	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(fmt.Sprint(i))
	rc.HeartbeatTimeout = 150 * time.Millisecond
	rc.ElectionTimeout = 150 * time.Millisecond
	rc.LeaderLeaseTimeout = 75 * time.Millisecond
	rc.CommitTimeout = 5 * time.Millisecond
	rc.LogOutput = io.Discard
	r, err := raft.NewRaft(rc, f, store, store, snaps, tr)
	if err != nil {
		t.Fatal(err)
	}
	c.nodes[i] = &Node{Raft: r, FSM: f, store: store, Config: Config{ID: fmt.Sprint(i), Peers: c.peers, MaxConcurrent: 100, EnableDemoFaults: true}}
	if bootstrap {
		conf := raft.Configuration{}
		for _, p := range c.peers {
			conf.Servers = append(conf.Servers, raft.Server{ID: raft.ServerID(p.ID), Address: raft.ServerAddress(p.Raft), Suffrage: raft.Voter})
		}
		if err = r.BootstrapCluster(conf).Error(); err != nil {
			t.Fatal(err)
		}
	}
}
func (c *testCluster) connect() {
	for i, tr := range c.trans {
		for j, peer := range c.trans {
			if i != j && tr != nil && peer != nil {
				tr.Connect(raft.ServerAddress(fmt.Sprint(j)), peer)
			}
		}
	}
}
func (c *testCluster) isolate(i int) {
	for j := range c.trans {
		if i != j {
			c.trans[i].Disconnect(raft.ServerAddress(fmt.Sprint(j)))
			c.trans[j].Disconnect(raft.ServerAddress(fmt.Sprint(i)))
		}
	}
}
func (c *testCluster) leader(t *testing.T, exclude int) int {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		for i, n := range c.nodes {
			if i != exclude && n != nil && n.Raft.State() == raft.Leader {
				if _, err := n.Submit(Command{Action: "barrier"}); err == nil {
					return i
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no majority leader elected")
	return -1
}
func submitOK(t *testing.T, n *Node, c Command) Result {
	t.Helper()
	r, err := n.Submit(c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (c *testCluster) equal(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a, _, e1 := c.nodes[0].FSM.Digest()
		b, _, e2 := c.nodes[1].FSM.Digest()
		d, _, e3 := c.nodes[2].FSM.Digest()
		if e1 == nil && e2 == nil && e3 == nil && a == b && b == d {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("replicas did not converge")
}

func TestClusterFailoverPartitionSnapshotRestart(t *testing.T) {
	c := newTestCluster(t)
	a := c.leader(t, -1)
	submitOK(t, c.nodes[a], Command{ID: "signup", Action: "register", Username: "alice", PasswordHash: "hash", Fingerprint: "credential"})
	op := Command{ID: "fund", UserID: 1, Action: "deposit", Account: "A1001", Amount: 1000}
	if r := submitOK(t, c.nodes[a], op); r.Balance != 1000 {
		t.Fatal(r)
	}
	// Commit succeeded but its response is treated as lost. Crash the leader;
	// retry on the successor must return the saved outcome, not add money.
	c.isolate(a)
	c.nodes[a].Close()
	c.nodes[a] = nil
	b := c.leader(t, a)
	if r := submitOK(t, c.nodes[b], op); r.Balance != 1000 {
		t.Fatal(r)
	}
	c.trans[a].Close()
	c.start(t, a, false)
	c.connect()
	c.equal(t)
	// Concurrent real proposals oversubscribe the same account.
	var wg sync.WaitGroup
	results := make(chan Result, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := c.nodes[b].Submit(Command{ID: fmt.Sprintf("w%d", i), UserID: 1, Action: "withdraw", Account: "A1001", Amount: 100})
			if err != nil {
				errs <- err
			} else {
				results <- r
			}
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	success := 0
	for r := range results {
		if r.Status == "success" {
			success++
		} else if r.Error != "insufficient_funds" {
			t.Fatal(r)
		}
	}
	if success != 10 {
		t.Fatal(success)
	}
	c.isolate(b)
	successor := c.leader(t, b)
	if _, err := c.nodes[b].Submit(Command{ID: "minority", UserID: 1, Action: "deposit", Account: "A1001", Amount: 999}); err == nil {
		t.Fatal("isolated node committed without quorum")
	}
	if _, err := c.nodes[b].Submit(Command{UserID: 1, Action: "balance", Account: "A1001"}); err == nil {
		t.Fatal("isolated node served authoritative balance")
	}
	if r := submitOK(t, c.nodes[successor], Command{ID: "majority", UserID: 1, Action: "deposit", Account: "A1001", Amount: 7}); r.Balance != 7 {
		t.Fatal(r)
	}
	c.connect()
	c.equal(t)
	// No communicating pair means no node can acknowledge a new commit.
	for i := range c.nodes {
		c.isolate(i)
	}
	for i, n := range c.nodes {
		if _, err := n.Submit(Command{ID: fmt.Sprintf("no-quorum-%d", i), UserID: 1, Action: "deposit", Account: "A1001", Amount: 1}); err == nil {
			t.Fatal("write succeeded without any majority")
		}
	}
	c.connect()
	successor = c.leader(t, -1)
	// Any uncertain proposal above could legitimately commit after healing;
	// reconcile it with the same ID, then make the subsequent balance explicit.
	for i := range c.nodes {
		submitOK(t, c.nodes[successor], Command{ID: fmt.Sprintf("no-quorum-%d", i), UserID: 1, Action: "deposit", Account: "A1001", Amount: 1})
	}
	if err := c.nodes[successor].Raft.Snapshot().Error(); err != nil {
		t.Fatal(err)
	}
	// Stop every process and reopen its complete persistent directory. This
	// covers snapshot restore, unsnapshotted followers, and retained outcomes.
	for i := range c.nodes {
		c.nodes[i].Close()
		c.nodes[i] = nil
		c.trans[i].Close()
	}
	for i := range c.nodes {
		c.start(t, i, false)
	}
	c.connect()
	leader := c.leader(t, -1)
	if r := submitOK(t, c.nodes[leader], op); r.Balance != 1000 {
		t.Fatalf("saved original response changed: %+v", r)
	}
	if r := submitOK(t, c.nodes[leader], Command{UserID: 1, Action: "balance", Account: "A1001"}); r.Balance != 10 {
		t.Fatal(r)
	}
	c.equal(t)
}

func TestClusterHTTPAuthIdempotencyAndRollback(t *testing.T) {
	c := newTestCluster(t)
	leader := c.leader(t, -1)
	key := bytes.Repeat([]byte{1}, 32)
	tokens, err := auth.New(auth.Config{Key: key, Issuer: "test", Audience: "test"})
	if err != nil {
		t.Fatal(err)
	}
	app := c.nodes[leader].App(tokens, key)
	call := func(method, path, id, token, body string, fail bool) (Result, int) {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", id)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if fail {
			req.Header.Set("X-Demo-Fail-Before-Commit", "true")
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var r Result
		if err = json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
		return r, resp.StatusCode
	}
	r, status := call("POST", "/users", "signup", "", `{"username":"alice","password":"password123"}`, false)
	if status != 201 {
		t.Fatal(status, r)
	}
	r, status = call("POST", "/users", "signup", "", `{"username":"alice","password":"password123"}`, false)
	if status != 201 || r.UserID != 1 {
		t.Fatal(status, r)
	}
	_, status = call("POST", "/users", "signup", "", `{"username":"alice","password":"different-password"}`, false)
	if status != 409 {
		t.Fatal(status)
	}
	token, _ := tokens.Generate(1)
	r, status = call("POST", "/accounts/A1001/deposit", "d1", token, `{"amount":100}`, false)
	if status != 200 || r.Balance != 100 {
		t.Fatal(status, r)
	}
	r, status = call("POST", "/accounts/A1001/withdraw", "fail", token, `{"amount":20}`, true)
	if status != 500 || r.Error != "injected_failure" {
		t.Fatal(status, r)
	}
	r, status = call("GET", "/accounts/A1001/balance", "", token, "", false)
	if status != 200 || r.Balance != 100 {
		t.Fatal(status, r)
	}
	r, status = call("GET", "/operations/d1", "", token, "", false)
	if status != 200 || r.Balance != 100 {
		t.Fatal(status, r)
	}
	for _, body := range []string{`{"amount":-1}`, `{"amount":0}`, `{"amount":"abc"}`, `{}`, `broken`} {
		_, status = call("POST", "/accounts/A1001/deposit", "bad", token, body, false)
		if status != 400 {
			t.Fatal(status, body)
		}
	}
	_, status = call("POST", "/accounts/Z999/deposit", "unknown", token, `{"amount":1}`, false)
	if status != 404 {
		t.Fatal(status)
	}
	_, status = call("POST", "/accounts/A1001/transfer", "x", token, `{"amount":1}`, false)
	if status != 400 {
		t.Fatal(status)
	}
	_, status = call("GET", "/accounts/A1001/balance", "", "", "", false)
	if status != 401 {
		t.Fatal(status)
	}
}
