package socketserver

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

type Environment struct {
	Config                           Config
	Address, DBPath, TLSCert, TLSKey string
	DBDriver, DBDSN                  string
}

func FromEnv(get func(string) string) (Environment, error) {
	e := Environment{Config: DefaultConfig(), Address: "127.0.0.1:4000", DBPath: "users.db"}
	if v := get("DB_PATH"); v != "" {
		e.DBPath = v
	}
	e.DBDriver = strings.ToLower(strings.TrimSpace(get("DB_DRIVER")))
	e.DBDSN = get("DB_DSN")
	if e.DBDSN == "" && (e.DBDriver == "" || e.DBDriver == "sqlite") {
		e.DBDSN = e.DBPath
	}
	if v := get("SOCKET_LISTEN_ADDR"); v != "" {
		e.Address = v
	}
	host, port, err := net.SplitHostPort(e.Address)
	if err != nil {
		return e, fmt.Errorf("SOCKET_LISTEN_ADDR: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return e, fmt.Errorf("SOCKET_LISTEN_ADDR requires port 1–65535")
	}
	for name, target := range map[string]*int{"MAX_CONCURRENT_REQUESTS": &e.Config.MaxConcurrentRequests, "SOCKET_MAX_CONNECTIONS": &e.Config.MaxConnections} {
		if raw := get(name); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v <= 0 {
				return e, fmt.Errorf("%s must be a positive integer", name)
			}
			*target = v
		}
	}
	for name, target := range map[string]*time.Duration{"SESSION_TTL": &e.Config.SessionTTL, "SESSION_CLEANUP_INTERVAL": &e.Config.CleanupInterval, "SOCKET_READ_TIMEOUT": &e.Config.ReadTimeout, "SOCKET_WRITE_TIMEOUT": &e.Config.WriteTimeout, "SOCKET_OPERATION_TIMEOUT": &e.Config.OperationTimeout, "SOCKET_DRAIN_TIMEOUT": &e.Config.DrainTimeout} {
		if raw := get(name); raw != "" {
			v, err := time.ParseDuration(raw)
			if err != nil || v <= 0 {
				return e, fmt.Errorf("%s must be a positive duration", name)
			}
			*target = v
		}
	}
	e.TLSCert = get("SOCKET_TLS_CERT")
	e.TLSKey = get("SOCKET_TLS_KEY")
	if (e.TLSCert == "") != (e.TLSKey == "") {
		return e, fmt.Errorf("SOCKET_TLS_CERT and SOCKET_TLS_KEY must be set together")
	}
	ip := net.ParseIP(host)
	if e.TLSCert == "" && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return e, fmt.Errorf("non-loopback sockets require SOCKET_TLS_CERT and SOCKET_TLS_KEY")
	}
	return e, e.Config.Validate()
}
