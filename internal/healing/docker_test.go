package healing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDockerCreateUsesDatabaseNetworkAndNoRestartPolicy(t *testing.T) {
	d := &Docker{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Path != "/containers/create" || r.URL.Query().Get("name") != "replacement" {
			t.Fatal("unexpected API request", r.URL)
		}
		var body struct {
			Config
			HostConfig struct {
				NetworkMode   string
				RestartPolicy struct{ Name string }
				Mounts        []struct {
					Source, Target string
					ReadOnly       bool
				}
			}
			NetworkingConfig struct{ EndpointsConfig map[string]Endpoint }
		}
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Fatal(e)
		}
		if body.Image != "sha256:pinned" || body.Env[0] != "DB_DSN=postgres://db-a" || body.Labels[SlotLabel] != "demo/shard-a1" || body.HostConfig.NetworkMode != "demo_bank" || body.HostConfig.RestartPolicy.Name != "no" || body.NetworkingConfig.EndpointsConfig["demo_bank"].Aliases[0] != "shard-a1" || !body.HostConfig.Mounts[0].ReadOnly {
			t.Fatalf("wrong create payload: %+v", body)
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"Id":"new-id"}`))}, nil
	})}}
	id, e := d.Create(context.Background(), "replacement", "demo/shard-a1", Template{Config: Config{Image: "sha256:pinned", Env: []string{"DB_DSN=postgres://db-a"}}, Network: "demo_bank", Alias: "shard-a1", Mounts: []Mount{{Type: "bind", Source: "/config.json", Destination: "/app/shards.json"}}})
	if e != nil || id != "new-id" {
		t.Fatal(id, e)
	}
}
func TestDockerDistinguishesMissingContainerFromDaemonFailure(t *testing.T) {
	for _, status := range []int{404, 500} {
		d := &Docker{HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("daemon error"))}, nil
		})}}
		c, e := d.Inspect(context.Background(), "missing")
		if status == 404 && (e != nil || c != nil) {
			t.Fatal("missing container not recognized")
		}
		if status == 500 && e == nil {
			t.Fatal("daemon failure hidden")
		}
	}
}
