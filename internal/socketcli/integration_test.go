package socketcli

import (
	"bytes"
	"context"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"github.com/azare242/gobank/internal/socketclient"
	"github.com/azare242/gobank/internal/socketserver"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInteractiveWalkthrough(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	defer sql.Close()
	config := socketserver.DefaultConfig()
	config.Logger = log.New(io.Discard, "", 0)
	server, err := socketserver.New(db, config)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		server.Shutdown(ctx)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := &socketclient.Client{Address: listener.Addr().String(), Timeout: time.Second}
	var out bytes.Buffer
	menu := Menu{In: strings.NewReader("1\nalice\npassword123\n2\nalice\npassword123\n1\n2\n3\n100\ny\n4\n40\ny\n2\n5\n4\n"), Out: &out, Client: client}
	if err = menu.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "balance: 60") || !strings.Contains(out.String(), "Logged out") {
		t.Fatal(out.String())
	}
	var account models.Account
	db.First(&account, "id = ?", "A1001")
	if account.Balance != 60 {
		t.Fatal(account)
	}
	var count int64
	db.Model(&models.Session{}).Count(&count)
	if count != 0 {
		t.Fatal("logout not persisted")
	}
	t.Log(out.String())
}
