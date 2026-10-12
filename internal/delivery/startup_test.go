// Package delivery verifies the shipped executables start with independent configuration.
package delivery

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func executable(t *testing.T, name string) string {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	path := filepath.Join(t.TempDir(), name+suffix)
	cmd := exec.Command("go", "build", "-o", path, "../../cmd/"+name)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v %s", name, err, output)
	}
	return path
}
func TestIndependentExecutablesAndCLI(t *testing.T) {
	clientBinary := executable(t, "socket-client")
	for _, name := range []string{"server", "socket-server"} {
		t.Run(name, func(t *testing.T) {
			binary := executable(t, name)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := l.Addr().String()
			l.Close()
			cmd := exec.Command(binary)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				key = strings.ToUpper(key)
				if strings.HasPrefix(key, "JWT_") || strings.HasPrefix(key, "SOCKET_") || key == "DB_PATH" || key == "DB_DRIVER" || key == "DB_DSN" || key == "LISTEN_ADDR" || key == "MAX_CONCURRENT_REQUESTS" || key == "SESSION_TTL" || key == "SESSION_CLEANUP_INTERVAL" {
					continue
				}
				cmd.Env = append(cmd.Env, entry)
			}
			cmd.Env = append(cmd.Env, "DB_PATH="+filepath.Join(t.TempDir(), "db"))
			if name == "server" {
				cmd.Env = append(cmd.Env, "LISTEN_ADDR="+address, "JWT_SECRET="+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "JWT_ISSUER=delivery", "JWT_AUDIENCE=delivery")
			} else {
				cmd.Env = append(cmd.Env, "SOCKET_LISTEN_ADDR="+address)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cmd.Process.Kill()
				cmd.Wait()
				if strings.Contains(output.String(), "password123") {
					t.Error("server printed password")
				}
			}()
			ready := false
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
				if err == nil {
					conn.Close()
					ready = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !ready {
				t.Fatal("server failed to listen")
			}
			if name == "server" {
				client := &http.Client{Timeout: time.Second}
				response, err := client.Get("http://" + address + "/health")
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				data, _ := io.ReadAll(response.Body)
				if response.StatusCode != 200 || !bytes.Contains(data, []byte(`"ok"`)) {
					t.Fatal(response.StatusCode, string(data))
				}
			} else {
				client := exec.Command(clientBinary, "-addr", address, "-timeout", "2s")
				client.Stdin = strings.NewReader("1\nalice\npassword123\n2\nalice\npassword123\n2\n3\n100\ny\n4\n40\ny\n5\n4\n")
				data, err := client.CombinedOutput()
				if err != nil {
					t.Fatal(err, string(data))
				}
				if !bytes.Contains(data, []byte("balance: 60")) || !bytes.Contains(data, []byte("Logged out")) || bytes.Contains(data, []byte("password123")) {
					t.Fatal(string(data))
				}
			}
		})
	}
}
