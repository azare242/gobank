package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/azare242/gobank/internal/auth"
	"github.com/azare242/gobank/internal/cluster"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	path := flag.String("config", "deploy/cluster/a.json", "Node configuration JSON")
	flag.Parse()
	b, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var cfg cluster.Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	tokens, err := auth.FromEnv()
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.Strict().DecodeString(os.Getenv("JWT_SECRET"))
	if err != nil {
		return err
	}
	n, err := cluster.Open(cfg)
	if err != nil {
		return err
	}
	defer n.Close()
	app := n.App(tokens, key)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() { <-ctx.Done(); _ = app.ShutdownWithTimeout(5 * time.Second) }()
	log.Printf("node=%s http=%s raft=%s data=%s", cfg.ID, cfg.HTTP, cfg.Raft, cfg.DataDir)
	return app.Listen(cfg.HTTP)
}
