package socketserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/azare242/gobank/internal/service"
	"gorm.io/gorm"
)

type Config struct {
	MaxConnections, MaxConcurrentRequests                     int
	ReadTimeout, WriteTimeout, OperationTimeout, DrainTimeout time.Duration
	SessionTTL, CleanupInterval                               time.Duration
	Logger                                                    *log.Logger
}

func DefaultConfig() Config {
	return Config{MaxConnections: 256, MaxConcurrentRequests: 100, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Second, OperationTimeout: 10 * time.Second, DrainTimeout: 15 * time.Second, SessionTTL: 15 * time.Minute, CleanupInterval: time.Minute, Logger: log.Default()}
}
func (c Config) Validate() error {
	if c.MaxConnections <= 0 || c.MaxConcurrentRequests <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.OperationTimeout <= 0 || c.DrainTimeout <= 0 || c.SessionTTL < time.Second || c.CleanupInterval <= 0 {
		return errors.New("connection/request limits and timeouts must be positive; session TTL must be at least one second")
	}
	return nil
}

type connection struct{ active bool }
type Server struct {
	config   Config
	bank     *service.Service
	sessions service.Sessions
	slots    chan struct{}
	mu       sync.Mutex
	conns    map[net.Conn]*connection
	listener net.Listener
	stopping bool
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
	limiter  loginLimiter
}

func New(db *gorm.DB, c Config) (*Server, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{config: c, bank: service.New(db), sessions: service.Sessions{DB: db, TTL: c.SessionTTL}, slots: make(chan struct{}, c.MaxConcurrentRequests), conns: make(map[net.Conn]*connection), ctx: ctx, cancel: cancel, limiter: loginLimiter{entries: make(map[string]attempt)}}, nil
}
func (s *Server) Serve(listener net.Listener) error {
	s.mu.Lock()
	if s.listener != nil || s.stopping {
		s.mu.Unlock()
		return errors.New("server already started or stopped")
	}
	s.listener = listener
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	cleanupCtx, stopCleanup := context.WithCancel(s.ctx)
	defer stopCleanup()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		s.sessions.RunCleanup(cleanupCtx, s.config.CleanupInterval, func(error) { s.config.Logger.Print("session cleanup failed") })
	}()
	defer func() { stopCleanup(); <-cleanupDone }()
	var backoff time.Duration
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			stopping := s.stopping
			s.mu.Unlock()
			if stopping || errors.Is(err, net.ErrClosed) {
				return nil
			}
			var temporary net.Error
			if errors.As(err, &temporary) && temporary.Temporary() {
				if backoff == 0 {
					backoff = 5 * time.Millisecond
				} else {
					backoff *= 2
				}
				if backoff > time.Second {
					backoff = time.Second
				}
				select {
				case <-s.ctx.Done():
					return nil
				case <-time.After(backoff):
					continue
				}
			}
			return err
		}
		backoff = 0
		s.mu.Lock()
		if s.stopping || len(s.conns) >= s.config.MaxConnections {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		state := &connection{}
		s.conns[conn] = state
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(conn, state)
	}
}
func (s *Server) handle(conn net.Conn, state *connection) {
	defer func() { conn.Close(); s.mu.Lock(); delete(s.conns, conn); s.mu.Unlock(); s.wg.Done() }()
	ip, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		ip = "unknown"
	}
	for {
		// Also bounds handshake writes when conn is a TLS connection.
		conn.SetDeadline(time.Now().Add(s.config.ReadTimeout))
		data, err := ReadFrame(conn, RequestLimit)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.config.Logger.Printf("connection read failed peer=%s", ip)
			}
			return
		}
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			return
		}
		state.active = true
		s.mu.Unlock()
		response := s.process(data, ip)
		encoded, err := json.Marshal(response)
		if err != nil {
			return
		}
		if len(encoded) > ResponseLimit {
			response = Response{Version: 1, RequestID: response.RequestID, Status: "error", StatusCode: 500, Error: "response_too_large", Message: "Response exceeds 1 MiB limit"}
			encoded, _ = json.Marshal(response)
		}
		conn.SetWriteDeadline(time.Now().Add(s.config.WriteTimeout))
		err = WriteFrame(conn, encoded, ResponseLimit)
		s.mu.Lock()
		state.active = false
		stopping := s.stopping
		s.mu.Unlock()
		if err != nil {
			s.config.Logger.Printf("response write failed request_id=%s peer=%s outcome=%s", response.RequestID, ip, response.Status)
			return
		}
		if stopping {
			return
		}
	}
}

// Shutdown stops admission and idle readers, drains admitted work, then unblocks all I/O.
// It always waits for Serve, cleanup, and connection handlers before returning.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	if s.listener != nil {
		s.listener.Close()
	}
	for conn, state := range s.conns {
		if !state.active {
			conn.Close()
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		s.mu.Lock()
		for conn := range s.conns {
			conn.Close()
		}
		s.mu.Unlock()
		<-done
		return ctx.Err()
	}
}
