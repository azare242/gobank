package socketclient

import (
	"context"
	"encoding/json"
	"github.com/azare242/gobank/internal/socketserver"
	"net"
	"testing"
	"time"
)

func TestResponseValidationAndReconnect(t *testing.T) {
	for _, bad := range []string{"id", "version", "json", "length", "disconnect", "status"} {
		t.Run(bad, func(t *testing.T) {
			calls := 0
			client := &Client{Timeout: time.Second, Dial: func(context.Context, string) (net.Conn, error) {
				calls++
				a, b := net.Pipe()
				go func() {
					defer b.Close()
					data, _ := socketserver.ReadFrame(b, socketserver.RequestLimit)
					var req socketserver.Request
					json.Unmarshal(data, &req)
					if calls == 1 {
						switch bad {
						case "json":
							socketserver.WriteFrame(b, []byte(`{`), socketserver.ResponseLimit)
							return
						case "length":
							b.Write([]byte{0, 16, 0, 1})
							return
						case "disconnect":
							return
						case "id":
							req.RequestID = "wrong"
						}
					}
					r := socketserver.Response{Version: 1, RequestID: req.RequestID, Status: "success", StatusCode: 200}
					if calls == 1 && bad == "version" {
						r.Version = 2
					}
					if calls == 1 && bad == "status" {
						r.StatusCode = 500
					}
					data, _ = json.Marshal(r)
					socketserver.WriteFrame(b, data, socketserver.ResponseLimit)
				}()
				return a, nil
			}}
			if _, err := client.Call(context.Background(), socketserver.Request{Operation: "health"}); err == nil {
				t.Fatal("invalid reply accepted")
			}
			if _, err := client.Call(context.Background(), socketserver.Request{Operation: "health"}); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatal("request replayed", calls)
			}
			client.Close()
		})
	}
}
func TestDeadlineAndCancellation(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	client := &Client{Timeout: 20 * time.Millisecond, Dial: func(context.Context, string) (net.Conn, error) { return a, nil }}
	start := time.Now()
	if _, err := client.Call(context.Background(), socketserver.Request{Operation: "health"}); err == nil {
		t.Fatal("missing timeout")
	}
	if time.Since(start) > time.Second {
		t.Fatal("deadline ignored")
	}
	a, b = net.Pipe()
	defer b.Close()
	client.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Call(ctx, socketserver.Request{Operation: "health"}); err == nil {
		t.Fatal("cancel ignored")
	}
	client.Close()
}
