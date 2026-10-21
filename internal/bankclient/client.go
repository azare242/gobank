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
	Status      string `json:"status"`
	Error       string `json:"error"`
	Message     string `json:"message"`
	OperationID string `json:"operation_id"`
	Account     string `json:"account"`
	UserID      uint   `json:"id,omitempty"`
	Balance     int64  `json:"balance"`
	Token       string `json:"access_token"`
	Leader      string `json:"leader"`
	HTTP        int    `json:"-"`
	Node        string `json:"-"`
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
	next := 0
	delay := 100 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return Reply{}, fmt.Errorf("pending operation %s: %w", id, err)
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
			return Reply{}, fmt.Errorf("pending operation %s: %w", id, ctx.Err())
		case <-timer.C:
		}
		if delay < time.Second {
			delay *= 2
		}
	}
}
