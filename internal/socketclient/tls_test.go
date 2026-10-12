package socketclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"github.com/azare242/gobank/internal/socketserver"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestTLSVerification(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	l, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.SetDeadline(time.Now().Add(time.Second))
			data, err := socketserver.ReadFrame(conn, socketserver.RequestLimit)
			if err == nil {
				var r socketserver.Request
				json.Unmarshal(data, &r)
				data, _ = json.Marshal(socketserver.Response{Version: 1, RequestID: r.RequestID, Status: "success", StatusCode: 200})
				socketserver.WriteFrame(conn, data, socketserver.ResponseLimit)
			}
			conn.Close()
		}
	}()
	roots := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	roots.AddCert(parsed)
	for _, tc := range []struct {
		config  *tls.Config
		success bool
	}{{&tls.Config{RootCAs: roots}, true}, {&tls.Config{RootCAs: x509.NewCertPool()}, false}, {&tls.Config{RootCAs: roots, ServerName: "wrong.example"}, false}, {&tls.Config{InsecureSkipVerify: true}, false}} {
		client := &Client{Address: l.Addr().String(), Timeout: time.Second, TLS: tc.config}
		_, err := client.Call(context.Background(), socketserver.Request{Operation: "health"})
		client.Close()
		if (err == nil) != tc.success {
			t.Fatal("TLS verification mismatch", err)
		}
	}
	l.Close()
	<-done
}
