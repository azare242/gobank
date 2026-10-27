package healing

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Slot struct {
	ID, Original, Name string
	Template           Template
	UnhealthySince     *time.Time
}
type State struct {
	Project       string
	Services      []string
	DesiredCounts map[string]int
	Slots         []Slot
}
type Controller struct {
	Engine Engine
	Path   string
	Grace  time.Duration
	State  State
	Crash  func(string)
	Event  func(string, string)
	lock   *os.File
}

func Open(path string, engine Engine, grace time.Duration) (*Controller, error) {
	if grace <= 0 {
		return nil, fmt.Errorf("grace must be positive")
	}
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, fmt.Errorf("controller state is already locked: %w", e)
	}
	c := &Controller{Engine: engine, Path: path, Grace: grace, lock: lock}
	b, e := os.ReadFile(path)
	if e == nil {
		e = json.Unmarshal(b, &c.State)
	} else if errors.Is(e, os.ErrNotExist) {
		e = nil
	}
	if e != nil {
		c.Close()
		return nil, e
	}
	return c, nil
}
func (c *Controller) Close() error { return c.lock.Close() }
func (c *Controller) save() error {
	b, e := json.MarshalIndent(c.State, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(c.Path), ".healing-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(f.Name(), c.Path); e != nil {
		return e
	}
	dir, e := os.Open(filepath.Dir(c.Path))
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func TemplateFrom(container *Container, service string) (Template, error) {
	t := Template{Config: container.Config, Alias: service, Mounts: container.Mounts}
	env := map[string]string{}
	for _, entry := range container.Config.Env {
		k, v, _ := strings.Cut(entry, "=")
		env[k] = v
	}
	t.Shard = env["SHARD_ID"]
	if t.Shard == "" || env["DB_DSN"] == "" || env["REPLICA_ID"] == "" {
		return t, fmt.Errorf("service %s is not a shard replica", service)
	}
	if len(container.NetworkSettings.Networks) != 1 {
		return t, fmt.Errorf("replicas must use one network")
	}
	for network, endpoint := range container.NetworkSettings.Networks {
		t.Network = network
		found := false
		for _, alias := range endpoint.Aliases {
			found = found || alias == service
		}
		if !found {
			return t, fmt.Errorf("missing service alias")
		}
	}
	if policy := container.HostConfig.RestartPolicy.Name; policy != "" && policy != "no" {
		return t, fmt.Errorf("Docker restart policies are prohibited")
	}
	if len(t.Config.Entrypoint) == 0 {
		return t, fmt.Errorf("missing image entrypoint")
	}
	for _, m := range t.Mounts {
		if m.Type != "bind" {
			return t, fmt.Errorf("only configuration bind mounts are supported")
		}
	}
	t.Config.Image = container.Image // Pin the exact image, not a mutable tag.
	t.Config.Labels = nil
	return t, nil
}
func (c *Controller) Bootstrap(ctx context.Context, project string, services []string) error {
	services = append([]string(nil), services...)
	sort.Strings(services)
	if project == "" || len(services) == 0 {
		return fmt.Errorf("project and services are required")
	}
	for i, s := range services {
		if s == "" || (i > 0 && s == services[i-1]) {
			return fmt.Errorf("duplicate or empty service")
		}
	}
	if c.State.Project != "" {
		if c.State.Project != project || !reflect.DeepEqual(c.State.Services, services) {
			return fmt.Errorf("desired configuration differs from persistent state")
		}
		return nil
	}
	state := State{Project: project, Services: services, DesiredCounts: map[string]int{}}
	for _, service := range services {
		ids, e := c.Engine.Find(ctx, project, service)
		if e != nil {
			return e
		}
		if len(ids) != 1 {
			return fmt.Errorf("bootstrap requires one container for %s", service)
		}
		container, e := c.Engine.Inspect(ctx, ids[0])
		if e != nil {
			return e
		}
		if container == nil {
			return fmt.Errorf("bootstrap container disappeared")
		}
		t, e := TemplateFrom(container, service)
		if e != nil {
			return e
		}
		id := project + "/" + service
		hash := sha256.Sum256([]byte(id))
		state.Slots = append(state.Slots, Slot{ID: id, Original: container.ID, Name: fmt.Sprintf("gobank-healed-%x", hash[:10]), Template: t})
		state.DesiredCounts[t.Shard]++
	}
	c.State = state
	return c.save()
}

// Reconcile isolates slot errors: a failed create must not block another shard.
func (c *Controller) Reconcile(ctx context.Context, now time.Time) error {
	var failures []error
	for i := range c.State.Slots {
		if e := c.reconcileSlot(ctx, &c.State.Slots[i], now); e != nil {
			failures = append(failures, fmt.Errorf("slot %s: %w", c.State.Slots[i].ID, e))
		}
	}
	return errors.Join(failures...)
}
func (c *Controller) reconcileSlot(ctx context.Context, s *Slot, now time.Time) error {
	replacement, e := c.Engine.Inspect(ctx, s.Name)
	if e != nil {
		return e
	}
	if replacement != nil && replacement.Config.Labels[SlotLabel] != s.ID {
		return fmt.Errorf("replacement name belongs to an unmanaged container")
	}
	old, e := c.Engine.Inspect(ctx, s.Original)
	if e != nil {
		return e
	}
	current := replacement
	if current == nil {
		current = old
	}
	if current != nil && c.Engine.Ready(ctx, current, s.Template) {
		if s.UnhealthySince != nil {
			s.UnhealthySince = nil
			return c.save()
		}
		return nil
	}
	if s.UnhealthySince == nil {
		s.UnhealthySince = &now
		if e = c.save(); e != nil {
			return e
		}
	}
	if replacement != nil && replacement.State.Status == "created" {
		if e = c.Engine.Start(ctx, replacement.ID); e != nil {
			return e
		}
		s.UnhealthySince = &now
		return c.save()
	}
	if now.Sub(*s.UnhealthySince) < c.Grace {
		return nil
	}
	for _, container := range []*Container{replacement, old} {
		if container != nil {
			if e = c.Engine.Remove(ctx, container.ID); e != nil {
				return e
			}
		}
	}
	id, e := c.Engine.Create(ctx, s.Name, s.ID, s.Template)
	if e != nil {
		return e
	}
	if c.Crash != nil {
		c.Crash("after-create")
	}
	if e = c.Engine.Start(ctx, id); e != nil {
		return e
	}
	s.UnhealthySince = &now
	if c.Event != nil {
		c.Event("replacement-started", s.ID)
	}
	return c.save()
}
