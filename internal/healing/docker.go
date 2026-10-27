// Package healing maintains durable application replica slots using Docker.
package healing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const SlotLabel = "io.gobank.healing.slot"

type Config struct {
	Image      string
	Env        []string
	Entrypoint []string
	Cmd        []string
	User       string
	WorkingDir string
	Labels     map[string]string
}
type Endpoint struct {
	Aliases   []string
	IPAddress string
}
type Mount struct {
	Type, Source, Destination string
	RW                        bool
}
type Container struct {
	ID     string `json:"Id"`
	Image  string
	Config Config
	State  struct {
		Status   string
		ExitCode int
	}
	HostConfig      struct{ RestartPolicy struct{ Name string } }
	Mounts          []Mount
	NetworkSettings struct{ Networks map[string]Endpoint }
}
type Template struct {
	Config                Config
	Network, Alias, Shard string
	Mounts                []Mount
}

type Engine interface {
	Inspect(context.Context, string) (*Container, error)
	Find(context.Context, string, string) ([]string, error)
	Create(context.Context, string, string, Template) (string, error)
	Start(context.Context, string) error
	Remove(context.Context, string) error
	Ready(context.Context, *Container, Template) bool
}

type Docker struct{ HTTP, Probe *http.Client }

func NewDocker(socket string) *Docker {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Docker{HTTP: &http.Client{Transport: transport, Timeout: 30 * time.Second}, Probe: &http.Client{Transport: &http.Transport{}, Timeout: 2 * time.Second}}
}

// The local daemon handles unversioned Engine API requests.
// Only standard container lifecycle fields are used.
func (d *Docker) request(ctx context.Context, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		b, e := json.Marshal(input)
		if e != nil {
			return 0, e
		}
		body = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if e != nil {
		return 0, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := d.HTTP.Do(req)
	if e != nil {
		return 0, fmt.Errorf("Docker connection: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("Docker %s failed: HTTP %d", method, resp.StatusCode)
	}
	if output != nil {
		e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(output)
	}
	return resp.StatusCode, e
}
func (d *Docker) Inspect(ctx context.Context, id string) (*Container, error) {
	var c Container
	status, e := d.request(ctx, "GET", "/containers/"+url.PathEscape(id)+"/json", nil, &c)
	if status == 404 {
		return nil, nil
	}
	return &c, e
}
func (d *Docker) Find(ctx context.Context, project, service string) ([]string, error) {
	f, _ := json.Marshal(map[string][]string{"label": {"com.docker.compose.project=" + project, "com.docker.compose.service=" + service}})
	var entries []struct {
		ID string `json:"Id"`
	}
	_, e := d.request(ctx, "GET", "/containers/json?all=true&filters="+url.QueryEscape(string(f)), nil, &entries)
	ids := []string{}
	for _, c := range entries {
		ids = append(ids, c.ID)
	}
	return ids, e
}
func (d *Docker) Create(ctx context.Context, name, slot string, t Template) (string, error) {
	config := t.Config
	config.Labels = map[string]string{SlotLabel: slot}
	type bind struct {
		Type, Source, Target string
		ReadOnly             bool
	}
	mounts := []bind{}
	for _, m := range t.Mounts {
		mounts = append(mounts, bind{m.Type, m.Source, m.Destination, !m.RW})
	}
	body := struct {
		Config
		HostConfig struct {
			NetworkMode   string
			RestartPolicy struct{ Name string }
			Mounts        []bind
		}
		NetworkingConfig struct{ EndpointsConfig map[string]Endpoint }
	}{Config: config}
	body.HostConfig.NetworkMode = t.Network
	body.HostConfig.RestartPolicy.Name = "no"
	body.HostConfig.Mounts = mounts
	body.NetworkingConfig.EndpointsConfig = map[string]Endpoint{t.Network: {Aliases: []string{t.Alias}}}
	var reply struct {
		ID string `json:"Id"`
	}
	_, e := d.request(ctx, "POST", "/containers/create?name="+url.QueryEscape(name), body, &reply)
	return reply.ID, e
}
func (d *Docker) Start(ctx context.Context, id string) error {
	_, e := d.request(ctx, "POST", "/containers/"+url.PathEscape(id)+"/start", nil, nil)
	return e
}
func (d *Docker) Remove(ctx context.Context, id string) error {
	status, e := d.request(ctx, "DELETE", "/containers/"+url.PathEscape(id)+"?force=true", nil, nil)
	if status == 404 {
		return nil
	}
	return e
}
func (d *Docker) Ready(ctx context.Context, c *Container, t Template) bool {
	if c.State.Status != "running" {
		return false
	}
	address := c.NetworkSettings.Networks[t.Network].IPAddress
	if address == "" {
		return false
	}
	req, e := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(address, "3000")+"/ready", nil)
	if e != nil {
		return false
	}
	resp, e := d.Probe.Do(req)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}
