package socketserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/azare242/gobank/internal/database"
	"github.com/azare242/gobank/internal/models"
	"gorm.io/gorm"
)

func setupLifecycle(t *testing.T, c Config) (*Server, net.Listener, *gorm.DB) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	c.Logger = log.New(io.Discard, "", 0)
	s, err := New(db, c)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.Shutdown(ctx)
		sql.Close()
	})
	return s, l, db
}
func startLifecycle(t *testing.T, s *Server, l net.Listener) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	t.Cleanup(func() {
		s.Shutdown(context.Background())
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
}
func TestConnectionCapAndFrameDeadline(t *testing.T) {
	c := DefaultConfig()
	c.MaxConnections = 1
	c.ReadTimeout = 150 * time.Millisecond
	s, l, _ := setupLifecycle(t, c)
	startLifecycle(t, s, l)
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if err = WriteFrame(conn, []byte(`{"version":1,"operation":"health"}`), RequestLimit); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadFrame(conn, ResponseLimit); err != nil {
		t.Fatal(err)
	}
	excess, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer excess.Close()
	excess.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = excess.Read(b[:]); err == nil {
		t.Fatal("excess connection accepted")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("cap failed to promptly close connection")
	}
	// Send only part of a header. The absolute frame deadline closes the slow peer.
	conn.Write([]byte{0})
	if _, err = conn.Read(b[:]); err == nil {
		t.Fatal("slow header accepted")
	}
	if e, ok := err.(net.Error); ok && e.Timeout() {
		t.Fatal("frame deadline did not close connection")
	}
}
func TestPanicReleasesSlotAndAudits(t *testing.T) {
	s, _, db := setupLifecycle(t, DefaultConfig())
	db.Callback().Query().Before("gorm:query").Register("panic-test", func(*gorm.DB) { panic("test") })
	result := s.process([]byte(`{"version":1,"operation":"login","username":"alice","password":"password123"}`), "ip")
	if result.StatusCode != 500 || len(s.slots) != 0 {
		t.Fatal(result, len(s.slots))
	}
	db.Callback().Query().Remove("panic-test")
	var audit models.AuditLog
	if err := db.First(&audit).Error; err != nil || audit.Result != "error" || audit.ErrorCode != "internal_error" {
		t.Fatal(audit, err)
	}
	if result = s.process([]byte(`{"version":1,"operation":"health"}`), "ip"); result.StatusCode != 200 {
		t.Fatal(result)
	}
}
func TestForcedShutdownCancelsOperation(t *testing.T) {
	c := DefaultConfig()
	c.OperationTimeout = time.Minute
	s, l, db := setupLifecycle(t, c)
	entered := make(chan struct{})
	db.Callback().Query().Before("gorm:query").Register("block-test", func(tx *gorm.DB) { close(entered); <-tx.Statement.Context.Done() })
	startLifecycle(t, s, l)
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	WriteFrame(conn, []byte(`{"version":1,"operation":"login","username":"alice","password":"password123"}`), RequestLimit)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("operation not started")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if len(s.slots) != 0 {
		t.Fatal("slot leaked")
	}
	db.Callback().Query().Remove("block-test")
}
func TestTLSListener(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	roots.AddCert(parsed)
	s, l, _ := setupLifecycle(t, DefaultConfig())
	startLifecycle(t, s, tls.NewListener(l, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}))
	conn, err := tls.Dial("tcp", l.Addr().String(), &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if err = WriteFrame(conn, []byte(`{"version":1,"operation":"health"}`), RequestLimit); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFrame(conn, ResponseLimit)
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	json.Unmarshal(data, &response)
	if response.StatusCode != 200 {
		t.Fatal(response)
	}
}
