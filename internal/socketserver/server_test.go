package socketserver

import (
	"context"
	"github.com/azare242/gobank/internal/database"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestIdleShutdown(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	defer sql.Close()
	c := DefaultConfig()
	c.MaxConnections = 1
	s, err := New(db, c)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- s.Serve(listener) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil && err.Error() != "server already started or stopped" {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("serve leaked")
	}
}
