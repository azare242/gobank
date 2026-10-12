package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"github.com/azare242/gobank/internal/socketcli"
	"github.com/azare242/gobank/internal/socketclient"
	"golang.org/x/term"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	address := flag.String("addr", "127.0.0.1:4000", "TCP server address")
	useTLS := flag.Bool("tls", false, "enable verified TLS")
	ca := flag.String("ca", "", "additional trusted PEM CA file (requires -tls)")
	serverName := flag.String("server-name", "", "TLS certificate hostname override (requires -tls)")
	timeout := flag.Duration("timeout", 15*time.Second, "positive dial/exchange timeout")
	flag.Parse()
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	if !*useTLS && (*ca != "" || *serverName != "") {
		return fmt.Errorf("-ca and -server-name require -tls")
	}
	client := &socketclient.Client{Address: *address, Timeout: *timeout}
	if *useTLS {
		config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: *serverName}
		if *ca != "" {
			roots, err := x509.SystemCertPool()
			if err != nil {
				roots = x509.NewCertPool()
			}
			data, err := os.ReadFile(*ca)
			if err != nil {
				return err
			}
			if !roots.AppendCertsFromPEM(data) {
				return fmt.Errorf("CA file contains no certificates")
			}
			config.RootCAs = roots
		}
		client.TLS = config
	}
	menu := &socketcli.Menu{In: os.Stdin, Out: os.Stdout, Client: client}
	fd := int(os.Stdin.Fd())
	var restore func()
	if term.IsTerminal(fd) {
		state, err := term.GetState(fd)
		if err != nil {
			return err
		}
		restore = func() { _ = term.Restore(fd, state) }
		defer restore()
		menu.ReadPassword = func() ([]byte, error) { return term.ReadPassword(fd) }
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			if restore != nil {
				restore()
			}
			os.Stdin.Close()
		case <-done:
		}
	}()
	return menu.Run(ctx)
}
