package healing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeEngine struct {
	containers       map[string]*Container
	healthy          map[string]bool
	creates, removes int
	failSlot         string
}

func replica(id, service string) *Container {
	c := &Container{ID: id, Image: "sha256:exact", Config: Config{Env: []string{"SHARD_ID=a", "DB_DSN=postgres://db-a", "REPLICA_ID=a1"}, Entrypoint: []string{"shard-server"}}}
	c.State.Status = "running"
	c.NetworkSettings.Networks = map[string]Endpoint{"demo_bank": {Aliases: []string{service}}}
	return c
}
func (f *fakeEngine) Inspect(_ context.Context, id string) (*Container, error) {
	return f.containers[id], nil
}
func (f *fakeEngine) Find(_ context.Context, _, service string) ([]string, error) {
	return []string{service}, nil
}
func (f *fakeEngine) Ready(_ context.Context, c *Container, _ Template) bool {
	return c.State.Status == "running" && f.healthy[c.ID]
}
func (f *fakeEngine) Create(_ context.Context, name, slot string, t Template) (string, error) {
	if strings.HasSuffix(slot, f.failSlot) && f.failSlot != "" {
		return "", errors.New("create failed")
	}
	f.creates++
	c := replica(name, t.Alias)
	c.Config = t.Config
	c.Config.Labels = map[string]string{SlotLabel: slot}
	c.State.Status = "created"
	f.containers[name] = c
	f.healthy[name] = true
	return name, nil
}
func (f *fakeEngine) Remove(_ context.Context, id string) error {
	f.removes++
	delete(f.containers, id)
	return nil
}
func (f *fakeEngine) Start(_ context.Context, id string) error {
	f.containers[id].State.Status = "running"
	return nil
}
func setup(t *testing.T, services ...string) (*Controller, *fakeEngine) {
	t.Helper()
	f := &fakeEngine{containers: map[string]*Container{}, healthy: map[string]bool{}}
	for _, s := range services {
		f.containers[s] = replica(s, s)
		f.healthy[s] = true
	}
	c, e := Open(filepath.Join(t.TempDir(), "state.json"), f, 10*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	if e = c.Bootstrap(context.Background(), "demo", services); e != nil {
		t.Fatal(e)
	}
	return c, f
}
func reconcile(t *testing.T, c *Controller, seconds int) {
	t.Helper()
	if e := c.Reconcile(context.Background(), time.Unix(int64(seconds), 0)); e != nil {
		t.Fatal(e)
	}
}
func TestHealthyAndTransientFailures(t *testing.T) {
	c, f := setup(t, "shard-a1")
	reconcile(t, c, 0)
	f.healthy["shard-a1"] = false
	reconcile(t, c, 1)
	reconcile(t, c, 9)
	f.healthy["shard-a1"] = true
	reconcile(t, c, 20)
	if f.creates != 0 || f.removes != 0 {
		t.Fatal("healthy replica replaced")
	}
}
func TestCrashAfterCreateAdoptsAndPreservesConfig(t *testing.T) {
	c, f := setup(t, "shard-a1", "shard-a2")
	f.healthy["shard-a1"] = false
	reconcile(t, c, 0)
	c.Crash = func(string) { panic("crash") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("missing crash")
			}
		}()
		_ = c.Reconcile(context.Background(), time.Unix(11, 0))
	}()
	c.Close()
	restarted, e := Open(c.Path, f, c.Grace)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	if e = restarted.Bootstrap(context.Background(), "demo", []string{"shard-a1", "shard-a2"}); e != nil {
		t.Fatal(e)
	}
	reconcile(t, restarted, 12)
	reconcile(t, restarted, 30)
	if f.creates != 1 || f.containers["shard-a2"] == nil {
		t.Fatal("duplicate creation or healthy replica removed")
	}
	slot := restarted.State.Slots[0]
	if slot.Template.Config.Image != "sha256:exact" || slot.Template.Network != "demo_bank" || slot.Template.Alias != "shard-a1" || restarted.State.DesiredCounts["a"] != 2 {
		t.Fatal("launch configuration or desired counts lost")
	}
}
func TestMissingAndRepeatedFailures(t *testing.T) {
	c, f := setup(t, "shard-a1")
	delete(f.containers, "shard-a1")
	reconcile(t, c, 0)
	reconcile(t, c, 11)
	name := c.State.Slots[0].Name
	f.healthy[name] = false
	reconcile(t, c, 22)
	reconcile(t, c, 40)
	if f.creates != 2 || len(f.containers) != 1 {
		t.Fatal("replacement count", f.creates)
	}
}
func TestSlotFailureDoesNotBlockOtherRepairs(t *testing.T) {
	c, f := setup(t, "shard-a1", "shard-a2")
	f.healthy = map[string]bool{}
	reconcile(t, c, 0)
	f.failSlot = "shard-a1"
	if e := c.Reconcile(context.Background(), time.Unix(11, 0)); e == nil {
		t.Fatal("expected failure")
	}
	if f.creates != 1 {
		t.Fatal("another failed slot blocked repair")
	}
}
func TestCollisionAndDesiredConfiguration(t *testing.T) {
	c, f := setup(t, "shard-a1")
	name := c.State.Slots[0].Name
	f.containers[name] = replica(name, "shard-a1")
	if e := c.Reconcile(context.Background(), time.Unix(0, 0)); e == nil {
		t.Fatal("unmanaged name accepted")
	}
	if f.removes != 0 {
		t.Fatal("removed unmanaged container")
	}
	if e := c.Bootstrap(context.Background(), "other", []string{"shard-a1"}); e == nil {
		t.Fatal("changed configuration accepted")
	}
	if _, e := Open(c.Path, f, c.Grace); e == nil {
		t.Fatal("concurrent controller accepted")
	}
}
func TestRejectRestartPolicy(t *testing.T) {
	_, f := setup(t, "shard-a1")
	container := f.containers["shard-a1"]
	container.HostConfig.RestartPolicy.Name = "always"
	if _, e := TemplateFrom(container, "shard-a1"); e == nil {
		t.Fatal("automatic restart accepted")
	}
}

func TestCorruptStateFailsClosed(t *testing.T) {
	c, f := setup(t, "shard-a1")
	c.Close()
	if e := os.WriteFile(c.Path, []byte("{broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if recovered, e := Open(c.Path, f, c.Grace); e == nil {
		recovered.Close()
		t.Fatal("corrupt state accepted")
	}
	if f.creates != 0 || f.removes != 0 {
		t.Fatal("Docker mutated with invalid state")
	}
}
