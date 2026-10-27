// Package bankclient retries logical operations without changing their IDs.
package bankclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Reply struct {
	TransactionID string `json:"transaction_id,omitempty"`
	Decision      string `json:"decision,omitempty"`
	Pending       bool   `json:"pending,omitempty"`
	Outcome       *Reply `json:"outcome,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error"`
	Message       string `json:"message"`
	OperationID   string `json:"operation_id"`
	Account       string `json:"account"`
	UserID        uint   `json:"id,omitempty"`
	Balance       int64  `json:"balance"`
	Token         string `json:"access_token"`
	Leader        string `json:"leader"`
	HTTP          int    `json:"-"`
	Node          string `json:"-"`
}
type Client struct {
	Servers []string
	Token   string
	HTTP    *http.Client
}

func New(servers, token string) (*Client, error) {
	c := &Client{Token: token, HTTP: &http.Client{Timeout: 4 * time.Second}}
	for _, s := range strings.Split(servers, ",") {
		s = strings.TrimRight(strings.TrimSpace(s), "/")
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			return nil, fmt.Errorf("invalid server URL %q", s)
		}
		c.Servers = append(c.Servers, s)
	}
	return c, nil
}

// Do returns only terminal outcomes. A context deadline means UNKNOWN, never
// proof of failure. Leader hints are accepted only for configured server URLs.
func (c *Client) Do(ctx context.Context, method, path, id string, body []byte) (Reply, error) {
	var pending Reply
	if method == "GET" && strings.HasPrefix(path, "/transactions/") {
		pending = Reply{Status: "pending", Pending: true, TransactionID: strings.TrimPrefix(path, "/transactions/")}
	}
	next := 0
	delay := 100 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return pending, fmt.Errorf("pending operation %s: %w", id, err)
		}
		base := c.Servers[next%len(c.Servers)]
		next++
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
		if err != nil {
			return Reply{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if id != "" {
			req.Header.Set("Idempotency-Key", id)
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		resp, err := c.HTTP.Do(req)
		if err == nil {
			b, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			var r Reply
			decodeErr := json.Unmarshal(b, &r)
			r.HTTP = resp.StatusCode
			r.Node = resp.Header.Get("X-Bank-Node")
			if replica := resp.Header.Get("X-Bank-Replica"); replica != "" {
				r.Node = replica
			}
			if readErr == nil && decodeErr == nil && resp.StatusCode == 200 && r.Status == "COMPLETED" && r.Outcome != nil {
				out := *r.Outcome
				out.TransactionID = r.TransactionID
				out.Decision = r.Decision
				out.HTTP = resp.StatusCode
				r = out
			}
			// Pending is a durable transaction reference, never a terminal success.
			if pending.TransactionID != "" && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 429 {
				return pending, fmt.Errorf("cannot reconcile pending transaction %s: HTTP %d", pending.TransactionID, resp.StatusCode)
			}
			if readErr == nil && decodeErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && r.Pending && r.TransactionID != "" {
				pending = r
				method, path, body = "GET", "/transactions/"+r.TransactionID, nil
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return r, fmt.Errorf("pending transaction %s: %w", r.TransactionID, ctx.Err())
				case <-timer.C:
				}
				continue
			}
			if readErr == nil && decodeErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && r.Status == "" && (r.Token != "" || r.Account != "") {
				r.Status = "success"
			}
			if readErr == nil && decodeErr == nil && r.Status != "" {
				if resp.StatusCode < 500 && resp.StatusCode != 429 || r.Error == "injected_failure" {
					return r, nil
				}
				for i, s := range c.Servers {
					if s == r.Leader {
						next = i
						break
					}
				}
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return pending, fmt.Errorf("pending operation %s: %w", id, ctx.Err())
		case <-timer.C:
		}
		if delay < time.Second {
			delay *= 2
		}
	}
}

// TransactionStatus returns the current durable state without waiting for completion.
func (c *Client) TransactionStatus(ctx context.Context, id string) (Reply, error) {
	if id == "" || strings.ContainsAny(id, "/?#") {
		return Reply{}, fmt.Errorf("invalid transaction ID")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", c.Servers[0]+"/transactions/"+id, nil)
	if e != nil {
		return Reply{}, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return Reply{}, e
	}
	defer resp.Body.Close()
	var out Reply
	e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	out.HTTP = resp.StatusCode
	if e == nil && resp.StatusCode >= 400 {
		e = fmt.Errorf("status: %s", out.Error)
	}
	return out, e
}

// Transfer retries one logical operation and reconciles its durable transaction
// ID after an uncertain reply. Caller journals the key before invoking it.
func (c *Client) Transfer(ctx context.Context, key, source, destination string, amount int64) (Reply, error) {
	if len(c.Servers) != 1 {
		return Reply{}, fmt.Errorf("transfers require one Gateway URL")
	}
	body, e := json.Marshal(map[string]any{"source": source, "destination": destination, "amount": amount})
	if e != nil {
		return Reply{}, e
	}
	return c.Do(ctx, "POST", "/transfers", key, body)
}
