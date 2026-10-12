// Package transporttest compares the independently runnable transports on real loopback listeners.
package transporttest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/azare242/gobank/internal/api"
	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/service"
	"github.com/azare242/gobank/internal/socketclient"
	"github.com/azare242/gobank/internal/socketserver"
	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	db   *gorm.DB
	call func(socketserver.Request) (socketclient.Response, error)
}

func setup(tb testing.TB, transport string, counts ...int) *fixture {
	workers := 1
	if len(counts) > 0 {
		workers = counts[0]
	}
	tb.Helper()
	db, err := database.Open(filepath.Join(tb.TempDir(), "db"))
	if err != nil {
		tb.Fatal(err)
	}
	sql, _ := db.DB()
	tb.Cleanup(func() { sql.Close() })
	bank := service.New(db)
	u, err := bank.CreateUser(context.Background(), service.Credentials{Username: "alice", Password: "password123"})
	if err != nil {
		tb.Fatal(err)
	}
	db.Model(&models.Account{}).Where("id = ?", u.AccountID).Update("balance", int64(1000))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	f := &fixture{db: db}
	if transport == "TCP" {
		config := socketserver.DefaultConfig()
		config.Logger = log.New(os.Stdout, "", 0)
		server, err := socketserver.New(db, config)
		if err != nil {
			tb.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		tb.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			server.Shutdown(ctx)
			if err := <-done; err != nil {
				tb.Error(err)
			}
		})
		login, err := (service.Sessions{DB: db, TTL: time.Hour}).Issue(context.Background(), u.ID, models.AuditLog{Action: "login", Method: "TCP", Path: "/login"})
		if err != nil {
			tb.Fatal(err)
		}
		// A bounded connection pool provides one connection per active worker.
		clients := make([]*socketclient.Client, 0, workers)
		pool := make(chan *socketclient.Client, workers)
		for i := 0; i < workers; i++ {
			c := &socketclient.Client{Address: listener.Addr().String(), Timeout: 10 * time.Second}
			pool <- c
			clients = append(clients, c)
		}
		tb.Cleanup(func() {
			for _, c := range clients {
				c.Close()
			}
		})
		f.call = func(r socketserver.Request) (socketclient.Response, error) {
			c := <-pool
			defer func() { pool <- c }()
			r.Token = login.Token
			return c.Call(context.Background(), r)
		}
	} else {
		tokens, err := auth.New(auth.Config{Key: []byte(strings.Repeat("k", 32)), Issuer: "comparison", Audience: "comparison"})
		if err != nil {
			tb.Fatal(err)
		}
		token, err := tokens.Generate(u.ID)
		if err != nil {
			tb.Fatal(err)
		}
		app := api.New(db, tokens)
		done := make(chan error, 1)
		go func() { done <- app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}) }()
		tb.Cleanup(func() {
			app.Shutdown()
			if err := <-done; err != nil {
				tb.Error(err)
			}
		})
		transport := &http.Transport{MaxConnsPerHost: workers, MaxIdleConnsPerHost: workers}
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
		tb.Cleanup(transport.CloseIdleConnections)
		f.call = func(r socketserver.Request) (socketclient.Response, error) {
			method, path := "GET", "/"+r.Operation
			var body []byte
			switch r.Operation {
			case "health", "me":
			case "list_users":
				path = "/users"
			case "get_user":
				path = fmt.Sprintf("/users/%d", r.ID)
			case "create_user", "login":
				method = "POST"
				if r.Operation == "create_user" {
					path = "/users"
				}
				body, _ = json.Marshal(service.Credentials{Username: r.Username, Password: r.Password})
			case "balance", "deposit", "withdraw":
				path = "/accounts/" + r.Account + "/" + r.Operation
				if r.Operation != "balance" {
					method = "POST"
					body, _ = json.Marshal(map[string]json.RawMessage{"amount": r.Amount})
				}
			default:
				return socketclient.Response{}, fmt.Errorf("unsupported comparison operation")
			}
			request, err := http.NewRequest(method, "http://"+listener.Addr().String()+path, bytes.NewReader(body))
			if err != nil {
				return socketclient.Response{}, err
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				return socketclient.Response{}, err
			}
			defer response.Body.Close()
			payload, err := io.ReadAll(response.Body)
			if err != nil {
				return socketclient.Response{}, err
			}
			out := socketclient.Response{StatusCode: response.StatusCode, Status: "success", Data: payload}
			if response.StatusCode >= 400 {
				json.Unmarshal(payload, &out)
			}
			return out, nil
		}
	}
	return f
}
func TestHTTPAndTCPBehaviorParity(t *testing.T) {
	httpFixture, tcpFixture := setup(t, "HTTP"), setup(t, "TCP")
	cases := []socketserver.Request{
		{Operation: "health"}, {Operation: "create_user", Username: " bob ", Password: "password123"}, {Operation: "create_user", Username: "bob", Password: "password123"},
		{Operation: "login", Username: "alice", Password: "wrongpass"}, {Operation: "me"}, {Operation: "list_users"}, {Operation: "get_user", ID: 2}, {Operation: "get_user", ID: 999},
		{Operation: "deposit", Account: "A1001", Amount: json.RawMessage(`-1`)}, {Operation: "deposit", Account: "A1001", Amount: json.RawMessage(`100`)},
		{Operation: "withdraw", Account: "A1001", Amount: json.RawMessage(`40`)}, {Operation: "withdraw", Account: "A1001", Amount: json.RawMessage(`1061`)},
		{Operation: "deposit", Account: "A1001", Amount: json.RawMessage(`9223372036854775807`)}, {Operation: "balance", Account: "A1002"}, {Operation: "balance", Account: "A1001"},
	}
	for _, r := range cases {
		a, err := httpFixture.call(r)
		if err != nil {
			t.Fatal(err)
		}
		b, err := tcpFixture.call(r)
		if err != nil {
			t.Fatal(err)
		}
		if a.StatusCode != b.StatusCode || a.Error != b.Error {
			t.Fatalf("%s differs: %d/%s vs %d/%s", r.Operation, a.StatusCode, a.Error, b.StatusCode, b.Error)
		}
		if a.Status == "success" {
			var x, y any
			json.Unmarshal(a.Data, &x)
			json.Unmarshal(b.Data, &y)
			if !reflect.DeepEqual(x, y) {
				t.Fatalf("%s data differs: %v vs %v", r.Operation, x, y)
			}
		}
	}
}
func BenchmarkTransports(b *testing.B) {
	for _, transport := range []string{"HTTP", "TCP"} {
		for _, op := range []string{"health", "balance", "deposit", "withdraw"} {
			for _, parallel := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/parallel=%t", transport, op, parallel)
				b.Run(name, func(b *testing.B) {
					workers := 1
					if parallel {
						workers = 8
					}
					f := setup(b, transport, workers)
					request := socketserver.Request{Operation: op, Account: "A1001", Amount: json.RawMessage(`1`)}
					// Warm the bounded pool before timing; setup and authentication are excluded.
					var warm sync.WaitGroup
					for i := 0; i < workers; i++ {
						warm.Add(1)
						go func() {
							defer warm.Done()
							if _, err := f.call(request); err != nil {
								b.Error(err)
							}
						}()
					}
					warm.Wait()
					sql, _ := f.db.DB()
					before := sql.Stats()
					var mu sync.Mutex
					durations := make([]time.Duration, 0, b.N)
					b.ReportAllocs()
					b.ResetTimer()
					run := func() {
						start := time.Now()
						r, err := f.call(request)
						elapsed := time.Since(start)
						if err != nil || r.StatusCode != 200 {
							b.Errorf("exchange failed: %v status %d", err, r.StatusCode)
						}
						mu.Lock()
						durations = append(durations, elapsed)
						mu.Unlock()
					}
					if parallel {
						b.SetParallelism(1)
						b.RunParallel(func(pb *testing.PB) {
							for pb.Next() {
								run()
							}
						})
					} else {
						for i := 0; i < b.N; i++ {
							run()
						}
					}
					b.StopTimer()
					after := sql.Stats()
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					if len(durations) > 0 {
						b.ReportMetric(float64(durations[len(durations)*95/100].Microseconds()), "p95-us")
					}
					b.ReportMetric(float64(after.WaitCount-before.WaitCount)/float64(b.N), "db-waits/op")
					b.ReportMetric(float64((after.WaitDuration-before.WaitDuration).Microseconds())/float64(b.N), "db-wait-us/op")
					b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "req/s")
				})
			}
		}
	}
}
