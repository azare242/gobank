package cluster

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	bolt "github.com/hashicorp/raft-boltdb/v2"
)

type Peer struct {
	ID   string `json:"id"`
	Raft string `json:"raft"`
	HTTP string `json:"http"`
}
type Config struct {
	ID               string `json:"id"`
	HTTP             string `json:"http"`
	Raft             string `json:"raft"`
	DataDir          string `json:"data_dir"`
	Bootstrap        bool   `json:"bootstrap"`
	Peers            []Peer `json:"peers"`
	MaxConcurrent    int    `json:"max_concurrent_requests"`
	EnableDemoFaults bool   `json:"enable_demo_faults"`
}

type Node struct {
	Raft      *raft.Raft
	FSM       *FSM
	Config    Config
	transport *raft.NetworkTransport
	store     *bolt.BoltStore
}

func Open(c Config) (_ *Node, err error) {
	if c.ID == "" || c.DataDir == "" || c.HTTP == "" || c.Raft == "" {
		return nil, fmt.Errorf("id, http, raft, data_dir are required")
	}
	if len(c.Peers) != 3 {
		return nil, fmt.Errorf("this lab requires exactly three voting peers")
	}
	ids := map[string]bool{}
	addrs := map[string]bool{}
	httpAddrs := map[string]bool{}
	found := false
	for _, p := range c.Peers {
		if p.ID == "" || p.Raft == "" || p.HTTP == "" || ids[p.ID] || addrs[p.Raft] || httpAddrs[p.HTTP] {
			return nil, fmt.Errorf("peers must have unique IDs/raft addresses and HTTP URLs")
		}
		ids[p.ID] = true
		addrs[p.Raft] = true
		httpAddrs[p.HTTP] = true
		if p.ID == c.ID {
			found = p.Raft == c.Raft
		}
	}
	if !found {
		return nil, fmt.Errorf("local ID/address must match peers")
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 100
	}
	if c.MaxConcurrent < 1 {
		return nil, fmt.Errorf("max_concurrent_requests must be positive")
	}
	if err = os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, err
	}
	f, err := OpenFSM(filepath.Join(c.DataDir, "bank.db"))
	if err != nil {
		return nil, err
	}
	n := &Node{FSM: f, Config: c}
	defer func() {
		if err != nil {
			n.Close()
		}
	}()
	n.store, err = bolt.NewBoltStore(filepath.Join(c.DataDir, "raft.db"))
	if err != nil {
		return nil, err
	}
	snaps, err := raft.NewFileSnapshotStore(c.DataDir, 2, os.Stderr)
	if err != nil {
		return nil, err
	}
	addr, err := net.ResolveTCPAddr("tcp", c.Raft)
	if err != nil {
		return nil, err
	}
	n.transport, err = raft.NewTCPTransport(c.Raft, addr, 3, time.Second, os.Stderr)
	if err != nil {
		return nil, err
	}
	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(c.ID)
	rc.SnapshotThreshold = 128
	rc.SnapshotInterval = 20 * time.Second
	hasState, err := raft.HasExistingState(n.store, n.store, snaps)
	if err != nil {
		return nil, err
	}
	// A nonempty application DB without its consensus history is not a fresh node.
	_, applied, err := f.Digest()
	if err != nil {
		return nil, err
	}
	if !hasState && applied != 0 {
		return nil, fmt.Errorf("application state exists without Raft history; restore the complete node directory")
	}
	n.Raft, err = raft.NewRaft(rc, f, n.store, n.store, snaps, n.transport)
	if err != nil {
		return nil, err
	}
	if c.Bootstrap && !hasState {
		conf := raft.Configuration{}
		for _, p := range c.Peers {
			conf.Servers = append(conf.Servers, raft.Server{ID: raft.ServerID(p.ID), Address: raft.ServerAddress(p.Raft), Suffrage: raft.Voter})
		}
		if err = n.Raft.BootstrapCluster(conf).Error(); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (n *Node) Close() {
	if n.Raft != nil {
		_ = n.Raft.Shutdown().Error()
	}
	if n.transport != nil {
		_ = n.transport.Close()
	}
	if n.store != nil {
		_ = n.store.Close()
	}
	if n.FSM != nil {
		_ = n.FSM.Close()
	}
}
func (n *Node) LeaderHTTP() string {
	_, id := n.Raft.LeaderWithID()
	for _, p := range n.Config.Peers {
		if p.ID == string(id) {
			return p.HTTP
		}
	}
	return ""
}
func (n *Node) Submit(c Command) (Result, error) {
	if n.Raft.State() != raft.Leader {
		return Result{}, raft.ErrNotLeader
	}
	b, err := json.Marshal(c)
	if err != nil {
		return Result{}, err
	}
	f := n.Raft.Apply(b, 2*time.Second)
	if err = f.Error(); err != nil {
		return Result{}, err
	}
	r, ok := f.Response().(Result)
	if !ok {
		return Result{}, fmt.Errorf("invalid FSM response")
	}
	return r, nil
}
