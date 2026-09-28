package tunnel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// HostedDial is the server-side equivalent of the CLI's client DialTunnel call.
// The callback stays at the owner boundary so tunnel does not import httpapi or
// client, and Forwarder keeps the same lazy per-connection dial behavior.
type HostedDial func(context.Context, string, ForwardSpec) (DialResult, error)

type hostedEntry struct {
	cancel context.CancelFunc
	fs     []*Forwarder
	regID  string
}

// HostedForwarderManager owns server-local listeners. It deliberately keeps only
// in-memory state: after a restart only presets marked autostart are restored by
// the server bootstrap path.
type HostedForwarderManager struct {
	mu       sync.Mutex
	ctx      context.Context
	registry *ForwarderRegistry
	dial     HostedDial
	entries  map[string]hostedEntry
}

func NewHostedForwarderManager(ctx context.Context, registry *ForwarderRegistry, dial HostedDial) *HostedForwarderManager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &HostedForwarderManager{ctx: ctx, registry: registry, dial: dial, entries: map[string]hostedEntry{}}
}

// Start binds every listener before publishing one hosted registry entry. A dial
// is intentionally not attempted here: Forwarder dials lazily on the first local
// connection, so autostart can succeed while a worker is still reconnecting.
func (m *HostedForwarderManager) Start(name, worker string, specs []ForwardSpec) (ForwarderRegistration, error) {
	if name == "" || worker == "" || len(specs) == 0 {
		return ForwarderRegistration{}, errors.New("hosted forward requires preset name, worker and specs")
	}
	m.mu.Lock()
	if _, ok := m.entries[name]; ok {
		m.mu.Unlock()
		return ForwarderRegistration{}, fmt.Errorf("hosted forward %q is already running", name)
	}
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(m.ctx)
	fs := make([]*Forwarder, 0, len(specs))
	cleanup := func() {
		cancel()
		for _, f := range fs {
			<-f.Ready
		}
	}
	for _, spec := range specs {
		f := &Forwarder{Spec: spec, Ready: make(chan error, 1), Dial: func(c context.Context) (DialResult, error) {
			if m.dial == nil {
				return DialResult{}, errors.New("hosted forward dial is not configured")
			}
			return m.dial(c, worker, spec)
		}}
		fs = append(fs, f)
		go func() { _ = f.Run(ctx) }()
		if err := <-f.Ready; err != nil {
			cleanup()
			return ForwarderRegistration{}, fmt.Errorf("hosted forward %q listen %s: %w", name, spec.ListenAddr(), err)
		}
	}
	reg := m.registry.Register("server", ForwarderRegistration{
		Worker: worker, Specs: specs, Host: "server", PID: os.Getpid(),
		StartedAt: time.Now(), Hosted: true,
	})
	m.mu.Lock()
	m.entries[name] = hostedEntry{cancel: cancel, fs: fs, regID: reg.ID}
	m.mu.Unlock()
	return reg, nil
}

func (m *HostedForwarderManager) Stop(name string) error {
	m.mu.Lock()
	entry, ok := m.entries[name]
	if ok {
		delete(m.entries, name)
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("hosted forward %q is not running", name)
	}
	entry.cancel()
	for _, f := range entry.fs {
		<-f.Ready
	}
	_ = m.registry.Delete(entry.regID, "server")
	return nil
}

// StopAll is used by server shutdown and is safe to call repeatedly.
func (m *HostedForwarderManager) StopAll() {
	m.mu.Lock()
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	m.mu.Unlock()
	for _, name := range names {
		_ = m.Stop(name)
	}
}
