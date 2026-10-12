package main

import (
	"context"
	"crypto/tls"
	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/socketserver"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	env, err := socketserver.FromEnv(os.Getenv)
	if err != nil {
		return err
	}
	var tlsConfig *tls.Config
	if env.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(env.TLSCert, env.TLSKey)
		if err != nil {
			return err
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
	}
	db, err := database.OpenConfig(database.Config{Driver: env.DBDriver, DSN: env.DBDSN})
	if err != nil {
		return err
	}
	sql, err := db.DB()
	if err != nil {
		return err
	}
	defer sql.Close()
	server, err := socketserver.New(db, env.Config)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", env.Address)
	if err != nil {
		return err
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	wg.Add(1)
	shutdownResult := make(chan error, 1)
	go func() {
		defer wg.Done()
		<-signals.Done()
		ctx, cancel := context.WithTimeout(context.Background(), env.Config.DrainTimeout)
		defer cancel()
		shutdownResult <- server.Shutdown(ctx)
	}()
	log.Printf("TCP server listening on %s", env.Address)
	serveErr := server.Serve(listener)
	stop()
	wg.Wait()
	shutdownErr := <-shutdownResult
	if serveErr != nil {
		return serveErr
	}
	return shutdownErr
}
