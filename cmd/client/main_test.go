package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/azare242/gobank/internal/bankclient"
)

func TestJournalVerifierCountsUniqueOutcomesAndPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	j := &journal{f: f}
	write := func(r record) {
		t.Helper()
		if err := j.write(r); err != nil {
			t.Fatal(err)
		}
	}
	write(record{Kind: "baseline", Account: "A1001", Initial: 100})
	write(record{Kind: "intent", ID: "d", Action: "deposit", Account: "A1001", Amount: 25})
	request := func(string, string, string, []byte) (bankclient.Reply, error) {
		return bankclient.Reply{Status: "success", HTTP: 200, Balance: 125}, nil
	}
	if err := verify(nil, path, request); err == nil {
		t.Fatal("pending intent passed verification")
	}
	r := bankclient.Reply{Status: "success", Balance: 125}
	write(record{Kind: "outcome", ID: "d", Reply: &r, HTTP: 200})
	write(record{Kind: "outcome", ID: "d", Reply: &r, HTTP: 200})
	write(record{Kind: "intent", ID: "w", Action: "withdraw", Account: "A1001", Amount: 500})
	rejected := bankclient.Reply{Status: "error", Error: "insufficient_funds"}
	write(record{Kind: "outcome", ID: "w", Reply: &rejected, HTTP: 409})
	if err := verify(nil, path, request); err != nil {
		t.Fatal(err)
	}
	request = func(string, string, string, []byte) (bankclient.Reply, error) {
		return bankclient.Reply{Status: "success", HTTP: 200, Balance: 150}, nil
	}
	if err := verify(nil, path, request); err == nil {
		t.Fatal("unexpected balance passed")
	}
}

func TestJournalRejectsTornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, []byte("{\"kind\":\"intent\"}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJournal(path); err == nil {
		t.Fatal("incomplete tail accepted")
	}
}
