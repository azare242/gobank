// Package socketclient implements sequential, bounded TCP exchanges without replaying requests.
package socketclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/azare242/gobank/internal/socketserver"
	"github.com/google/uuid"
	"net"
	"sync"
	"time"
	"unicode/utf8"
)

type Response struct {
	Version    int             `json:"version"`
	RequestID  string          `json:"request_id"`
	Status     string          `json:"status"`
	StatusCode int             `json:"status_code"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      string          `json:"error,omitempty"`
	Message    string          `json:"message,omitempty"`
}
type Client struct {
	Address string
	Timeout time.Duration
	TLS     *tls.Config
	Dial    func(context.Context, string) (net.Conn, error)
	mu      sync.Mutex
	conn    net.Conn
}

func (c *Client) Close() error { c.mu.Lock(); defer c.mu.Unlock(); return c.disconnect() }
func (c *Client) disconnect() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}
func (c *Client) Call(ctx context.Context, request socketserver.Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var response Response
	if c.Timeout <= 0 {
		return response, errors.New("client timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	request.Version = 1
	request.RequestID = uuid.NewString()
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > socketserver.RequestLimit {
		return response, errors.New("invalid or oversized request")
	}
	if c.conn == nil {
		if c.Dial != nil {
			c.conn, err = c.Dial(ctx, c.Address)
		} else {
			dialer := &net.Dialer{}
			if c.TLS != nil {
				config := c.TLS.Clone()
				if config.InsecureSkipVerify {
					return response, errors.New("TLS certificate verification is required")
				}
				c.conn, err = (&tls.Dialer{NetDialer: dialer, Config: config}).DialContext(ctx, "tcp", c.Address)
			} else {
				c.conn, err = dialer.DialContext(ctx, "tcp", c.Address)
			}
		}
		if err != nil {
			return response, fmt.Errorf("connection failed: %w", err)
		}
	}
	conn := c.conn
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { conn.Close(); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	err = socketserver.WriteFrame(conn, payload, socketserver.RequestLimit)
	if err == nil {
		payload, err = socketserver.ReadFrame(conn, socketserver.ResponseLimit)
	}
	if err != nil {
		c.disconnect()
		return response, fmt.Errorf("exchange failed: %w", err)
	}
	if !utf8.Valid(payload) || json.Unmarshal(payload, &response) != nil || response.Version != 1 || response.RequestID != request.RequestID ||
		(response.Status != "success" && response.Status != "error") ||
		(response.Status == "success" && (response.StatusCode < 200 || response.StatusCode >= 300)) ||
		(response.Status == "error" && (response.StatusCode < 400 || response.StatusCode > 599 || response.Error == "")) {
		c.disconnect()
		return Response{}, errors.New("invalid response envelope or request ID")
	}
	return response, nil
}
