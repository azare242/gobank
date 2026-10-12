package socketserver

import (
	"testing"
	"time"
)

func TestConfiguration(t *testing.T) {
	empty := func(string) string { return "" }
	env, err := FromEnv(empty)
	if err != nil || env.Address != "127.0.0.1:4000" || env.Config.SessionTTL != 15*time.Minute {
		t.Fatal(env, err)
	}
	for _, entry := range [][2]string{{"MAX_CONCURRENT_REQUESTS", "0"}, {"SOCKET_MAX_CONNECTIONS", "-1"}, {"SESSION_TTL", "0s"}, {"SESSION_TTL", "1ms"}, {"SOCKET_READ_TIMEOUT", "bad"}, {"SOCKET_LISTEN_ADDR", "0.0.0.0:4000"}, {"SOCKET_LISTEN_ADDR", "bad"}, {"SOCKET_TLS_CERT", "cert"}} {
		if _, err := FromEnv(func(name string) string {
			if name == entry[0] {
				return entry[1]
			}
			return ""
		}); err == nil {
			t.Fatal("accepted", entry)
		}
	}
	if _, err := FromEnv(func(name string) string {
		switch name {
		case "SOCKET_LISTEN_ADDR":
			return "0.0.0.0:4000"
		case "SOCKET_TLS_CERT":
			return "cert"
		case "SOCKET_TLS_KEY":
			return "key"
		}
		return ""
	}); err != nil {
		t.Fatal(err)
	}
}
