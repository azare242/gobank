package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/azare242/gobank/internal/bankclient"
	"github.com/google/uuid"
)

type record struct {
	Kind          string            `json:"kind"`
	ID            string            `json:"operation_id,omitempty"`
	Action        string            `json:"action,omitempty"`
	Account       string            `json:"account,omitempty"`
	Destination   string            `json:"destination,omitempty"`
	TransactionID string            `json:"transaction_id,omitempty"`
	Amount        int64             `json:"amount,omitempty"`
	Initial       int64             `json:"initial_balance,omitempty"`
	Reply         *bankclient.Reply `json:"reply,omitempty"`
	HTTP          int               `json:"http,omitempty"`
	UserID        uint              `json:"user_id,omitempty"`
}
type journal struct {
	mu sync.Mutex
	f  *os.File
}

func (j *journal) write(r record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = j.f.Write(b); err != nil {
		return err
	}
	return j.f.Sync()
}
func output(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	servers := flag.String("servers", "http://192.168.56.11:3000,http://192.168.56.12:3000,http://192.168.56.13:3000", "Comma-separated server URLs")
	verifyFile := flag.String("verify-file", "", "JSON file containing expected balances and owner tokens for multi-account verification")
	gatewayURL := flag.String("gateway", "", "Single Assignment 2 Gateway URL (overrides -servers)")
	destination := flag.String("destination", "", "Destination account for transfer or transfer workload")
	transaction := flag.String("transaction", "", "Transaction ID for status/reconciliation")
	action := flag.String("action", "balance", "register, login, balance, deposit, withdraw, transfer, status, workload, reconcile")
	username := flag.String("username", "demo", "Username; password comes from BANK_PASSWORD")
	account := flag.String("account", "", "Account ID (otherwise read /me)")
	amount := flag.Int64("amount", 1, "Positive amount per mutation")
	count := flag.Int("count", 200, "Workload operations, alternating deposits/withdrawals")
	pattern := flag.String("pattern", "mixed", "Workload pattern: mixed, withdraw, deposit")
	workers := flag.Int("workers", 4, "Concurrent workload workers")
	interval := flag.Duration("interval", 200*time.Millisecond, "Delay between operations per worker")
	deadline := flag.Duration("timeout", 45*time.Second, "Retry deadline per logical operation")
	path := flag.String("journal", "client-journal.jsonl", "Durable intent/outcome journal; use a fresh file per workload")
	id := flag.String("id", "", "Explicit operation ID (otherwise generated once)")
	flag.Parse()
	if *pattern != "mixed" && *pattern != "withdraw" && *pattern != "deposit" {
		return fmt.Errorf("pattern must be mixed, withdraw, or deposit")
	}
	if *deadline <= 0 || *amount <= 0 || *count < 1 || *workers < 1 || *interval < 0 {
		return fmt.Errorf("timeout, amount, count and workers must be positive; interval must be nonnegative")
	}
	if *gatewayURL != "" {
		if strings.Contains(*gatewayURL, ",") {
			return fmt.Errorf("-gateway accepts exactly one URL")
		}
		*servers = *gatewayURL
	}
	c, err := bankclient.New(*servers, os.Getenv("BANK_TOKEN"))
	if err != nil {
		return err
	}
	if (*action == "transfer" || *destination != "") && len(c.Servers) != 1 {
		return fmt.Errorf("transfers require one Gateway URL; use -gateway")
	}
	request := func(method, path, id string, body []byte) (bankclient.Reply, error) {
		ctx, cancel := context.WithTimeout(context.Background(), *deadline)
		defer cancel()
		return c.Do(ctx, method, path, id, body)
	}
	if *action == "register" || *action == "login" {
		password := os.Getenv("BANK_PASSWORD")
		if password == "" {
			return fmt.Errorf("set BANK_PASSWORD")
		}
		b, _ := json.Marshal(map[string]string{"username": *username, "password": password})
		route := "/login"
		op := ""
		if *action == "register" {
			route = "/users"
			op = *id
			if op == "" {
				op = uuid.NewString()
			}
			fmt.Fprintln(os.Stderr, "registration operation ID:", op, "(reuse with -id if interrupted)")
		}
		r, err := request("POST", route, op, b)
		if err != nil {
			return err
		}
		output(r)
		if r.HTTP >= 400 {
			return fmt.Errorf("%s: %s", r.Error, r.Message)
		}
		return nil
	}
	if *action == "verify" {
		if *gatewayURL == "" || *verifyFile == "" {
			return fmt.Errorf("verify requires -gateway and -verify-file")
		}
		b, e := os.ReadFile(*verifyFile)
		if e != nil {
			return e
		}
		var config struct {
			Expected map[string]int64  `json:"expected"`
			Tokens   map[string]string `json:"tokens"`
		}
		if e = json.Unmarshal(b, &config); e != nil {
			return e
		}
		if len(config.Expected) == 0 {
			return fmt.Errorf("expected balances are required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), *deadline)
		defer cancel()
		total, e := bankclient.VerifyBalances(ctx, *gatewayURL, config.Tokens, config.Expected)
		if e != nil {
			return e
		}
		output(map[string]any{"verified": true, "total": total.String(), "accounts": len(config.Expected)})
		return nil
	}
	if c.Token == "" {
		return fmt.Errorf("set BANK_TOKEN to the login access_token")
	}
	if *action == "status" {
		if *transaction == "" {
			return fmt.Errorf("-transaction is required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), *deadline)
		defer cancel()
		r, e := c.TransactionStatus(ctx, *transaction)
		output(r)
		return e
	}
	var ownerID uint
	{
		r, err := request("GET", "/me", "", nil)
		if err != nil {
			return err
		}
		if r.HTTP >= 400 {
			return fmt.Errorf("profile: %s", r.Error)
		}
		if *account != "" && *account != r.Account {
			return fmt.Errorf("requested account does not belong to the logged-in user")
		}
		*account = r.Account
		ownerID = r.UserID
	}
	if *action == "balance" {
		r, err := request("GET", "/accounts/"+*account+"/balance", "", nil)
		if err != nil {
			return err
		}
		output(r)
		if r.HTTP >= 400 {
			return fmt.Errorf("balance: %s", r.Error)
		}
		return nil
	}
	if *action != "deposit" && *action != "withdraw" && *action != "transfer" && *action != "workload" && *action != "reconcile" {
		return fmt.Errorf("unsupported action %q", *action)
	}
	if *action == "workload" {
		info, err := os.Stat(*path)
		if err == nil && info.Size() > 0 {
			return fmt.Errorf("workload requires a fresh journal; reconcile existing file first")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// A trailing partial record after a client crash is rejected on replay. Do not
	// append beyond it and accidentally hide earlier complete intents.
	if *action != "workload" {
		rows, err := readJournal(*path)
		if err != nil && (!os.IsNotExist(err) || *action == "reconcile") {
			return err
		}
		for _, row := range rows {
			if row.Kind == "owner" && row.UserID != ownerID {
				return fmt.Errorf("journal belongs to a different user")
			}
			if (row.Kind == "intent" || row.Kind == "baseline") && row.Account != *account {
				return fmt.Errorf("journal contains another user's account")
			}
		}
	}
	f, err := os.OpenFile(*path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	j := &journal{f: f}
	if err = j.write(record{Kind: "owner", UserID: ownerID, Account: *account}); err != nil {
		return err
	}
	execute := func(r record, writeIntent bool) error {
		if writeIntent {
			if err := j.write(r); err != nil {
				return err
			}
		}
		b, _ := json.Marshal(map[string]int64{"amount": r.Amount})
		route := "/accounts/" + r.Account + "/" + r.Action
		if r.Action == "transfer" {
			route = "/transfers"
			b, _ = json.Marshal(map[string]any{"source": r.Account, "destination": r.Destination, "amount": r.Amount})
		}
		var response bankclient.Reply
		var err error
		if r.TransactionID != "" {
			response, err = request("GET", "/transactions/"+r.TransactionID, "", nil)
		} else {
			response, err = request("POST", route, r.ID, b)
		}
		if err != nil {
			if response.TransactionID != "" {
				if e := j.write(record{Kind: "pending", ID: r.ID, TransactionID: response.TransactionID}); e != nil {
					return e
				}
			}
			output(map[string]string{"status": "pending", "operation_id": r.ID, "transaction_id": response.TransactionID})
			return err
		}
		if err = j.write(record{Kind: "outcome", ID: r.ID, Reply: &response, HTTP: response.HTTP}); err != nil {
			return err
		}
		output(map[string]any{"operation_id": r.ID, "node": response.Node, "action": r.Action, "status": response.Status, "error": response.Error, "balance": response.Balance})
		return nil
	}
	if *action == "reconcile" {
		recs, err := readJournal(*path)
		if err != nil {
			return err
		}
		intents := map[string]record{}
		done := map[string]bool{}
		for _, r := range recs {
			if r.Kind == "intent" {
				intents[r.ID] = r
			}
			if r.Kind == "pending" {
				intent := intents[r.ID]
				intent.TransactionID = r.TransactionID
				intents[r.ID] = intent
			}
			if r.Kind == "outcome" {
				done[r.ID] = true
			}
		}
		for id, r := range intents {
			if !done[id] {
				if err = execute(r, false); err != nil {
					return err
				}
			}
		}
		return verify(c, *path, request)
	}
	if *action != "workload" {
		op := *id
		if op == "" {
			op = uuid.NewString()
		}
		rows, err := readJournal(*path)
		if err != nil {
			return err
		}
		for _, old := range rows {
			if old.Kind == "intent" && old.ID == op && (old.Action != *action || old.Account != *account || old.Amount != *amount || old.Destination != *destination) {
				return fmt.Errorf("operation ID already journaled with another payload")
			}
		}
		return execute(record{Kind: "intent", ID: op, Action: *action, Account: *account, Destination: *destination, Amount: *amount}, true)
	}
	initial, err := request("GET", "/accounts/"+*account+"/balance", "", nil)
	if err != nil {
		return err
	}
	if initial.HTTP != 200 {
		return fmt.Errorf("initial balance: %s", initial.Error)
	}
	if err = j.write(record{Kind: "baseline", Account: *account, Initial: initial.Balance}); err != nil {
		return err
	}
	jobs := make(chan int)
	errs := make(chan error, *count)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				a := "deposit"
				if k%2 == 1 {
					a = "withdraw"
				}
				if *pattern != "mixed" {
					a = *pattern
				}
				if *destination != "" {
					a = "transfer"
				}
				if err := execute(record{Kind: "intent", ID: uuid.NewString(), Action: a, Account: *account, Destination: *destination, Amount: *amount}, true); err != nil {
					errs <- err
				}
				time.Sleep(*interval)
			}
		}()
	}
	for k := 0; k < *count; k++ {
		jobs <- k
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if len(errs) > 0 {
		return fmt.Errorf("%d operations pending or journal writes failed; run -action reconcile using this journal", len(errs))
	}
	return verify(c, *path, request)
}

func readJournal(path string) ([]record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return nil, fmt.Errorf("journal has an incomplete final line: back it up, remove only that incomplete line, then reconcile complete intents")
	}
	s := bufio.NewScanner(bytesReader(b))
	s.Buffer(make([]byte, 4096), 1<<20)
	var rows []record
	for s.Scan() {
		var r record
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("journal: %w", err)
		}
		rows = append(rows, r)
	}
	return rows, s.Err()
}

// A string reader keeps journal replay independent from open append handles.
func bytesReader(b []byte) *strings.Reader { return strings.NewReader(string(b)) }

func verify(c *bankclient.Client, path string, request func(string, string, string, []byte) (bankclient.Reply, error)) error {
	rows, err := readJournal(path)
	if err != nil {
		return err
	}
	intents := map[string]record{}
	outcomes := map[string]record{}
	var base *record
	for _, r := range rows {
		switch r.Kind {
		case "baseline":
			copy := r
			base = &copy
		case "intent":
			if old, ok := intents[r.ID]; ok && (old.Action != r.Action || old.Account != r.Account || old.Amount != r.Amount || old.Destination != r.Destination) {
				return fmt.Errorf("journal operation ID reused with another payload")
			}
			intents[r.ID] = r
		case "outcome":
			outcomes[r.ID] = r
		}
	}
	pending := 0
	delta := new(big.Int)
	success := 0
	for id, r := range intents {
		o, ok := outcomes[id]
		if !ok {
			pending++
			continue
		}
		if o.Reply != nil && o.Reply.Status == "success" {
			success++
			if r.Action == "deposit" {
				delta.Add(delta, big.NewInt(r.Amount))
			} else {
				delta.Sub(delta, big.NewInt(r.Amount))
			}
		}
	}
	if pending > 0 {
		return fmt.Errorf("%d pending operations remain", pending)
	}
	if base == nil {
		output(map[string]any{"reconciled": true, "unique_operations": len(intents), "successful": success, "net_change": delta.String()})
		return nil
	}
	r, err := request("GET", "/accounts/"+base.Account+"/balance", "", nil)
	if err != nil {
		return err
	}
	if r.HTTP != 200 {
		return fmt.Errorf("balance: %s", r.Error)
	}
	expected := new(big.Int).Add(big.NewInt(base.Initial), delta)
	match := expected.Cmp(big.NewInt(r.Balance)) == 0
	output(map[string]any{"initial_balance": base.Initial, "expected_balance": expected, "actual_balance": r.Balance, "unique_operations": len(intents), "successful": success, "verified": match})
	if !match {
		return fmt.Errorf("balance invariant failed (ensure no other workload used this account)")
	}
	return nil
}
