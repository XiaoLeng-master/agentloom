// Package config implements the agent hot-reload mechanism: a Manager holds
// the active config per agent name and swaps it via Reload, while a per-version
// reference count guarantees in-flight requests against the old config finish
// before that config is finalized. This is the seam the evaluation harness uses
// to ship an optimization as a zero-interrupt gray rollout.
package config

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Config is a versioned, immutable agent configuration. Reloads create a new
// Config; the Manager never mutates one in place.
type Config struct {
	Version       int // monotonic per name; lets a handle detect it is stale
	Name          string
	SystemPrompt  string
	Model         string
	MaxIter       int
	Tools         []string
	ChunkStrategy string
	PromptMode    string
	TopK          int
}

// ErrUnknownAgent is returned by Acquire when no config is registered for name.
var ErrUnknownAgent = errors.New("config: unknown agent")

// AgentHandle is a reference to a config version. Acquire increments the
// refcount; Release decrements it. A Reload supersedes the old entry, but the
// old entry stays alive until its refcount drains to zero.
type AgentHandle struct {
	cfg      Config
	refs     *int32       // points at the entry's refs counter
	stale    *atomic.Bool // points at the entry's stale flag
	released atomic.Bool
}

// Config returns the config this handle was acquired against.
func (h *AgentHandle) Config() Config { return h.cfg }

// IsStale reports whether a newer version has superseded this handle's config.
func (h *AgentHandle) IsStale() bool {
	if h.stale == nil {
		return false
	}
	return h.stale.Load()
}

// Release decrements the refcount. Always call once per Acquire (defer it).
func (h *AgentHandle) Release() {
	if h.refs != nil && h.released.CompareAndSwap(false, true) {
		atomic.AddInt32(h.refs, -1)
	}
}

type entry struct {
	cfg   Config
	refs  int32
	stale atomic.Bool
}

// Manager owns the active config per agent name and performs hot-reload swaps.
type Manager struct {
	mu      sync.RWMutex
	active  map[string]*entry
	drained map[string][]*entry // superseded entries awaiting drain
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{active: make(map[string]*entry), drained: make(map[string][]*entry)}
}

// Register installs the initial config for name. Use Reload to update it.
func (m *Manager) Register(name string, cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg.Version = 1
	m.active[name] = &entry{cfg: cfg}
}

// Acquire returns a handle to the active config for name. The refcount is
// incremented so a concurrent Reload will drain the old version instead of
// tearing it down under running requests.
func (m *Manager) Acquire(name string) (*AgentHandle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.active[name]
	if !ok {
		return nil, ErrUnknownAgent
	}
	atomic.AddInt32(&e.refs, 1)
	return &AgentHandle{cfg: e.cfg, refs: &e.refs, stale: &e.stale}, nil
}

// Reload installs a new config for name and supersedes the previous one. New
// Acquires hit the new config; in-flight handles against the old config keep
// running. Returns the in-flight refcount of the superseded version (0 if none).
func (m *Manager) Reload(name string, cfg Config) int {
	m.mu.Lock()
	old, ok := m.active[name]
	if ok {
		cfg.Version = old.cfg.Version + 1
	} else {
		cfg.Version = 1
	}
	e := &entry{cfg: cfg}
	m.active[name] = e
	if ok {
		m.drained[name] = append(m.drained[name], old)
		old.stale.Store(true)
	}
	m.mu.Unlock()
	if ok {
		return int(atomic.LoadInt32(&old.refs))
	}
	return 0
}

// Drain blocks until every superseded entry for name has zero in-flight refs
// or the timeout elapses. Returns the max remaining refcount across stale
// entries (0 = clean drain). This is the "wait for in-flight to finish" half
// of the zero-interrupt swap; the other half is Reload creating the new entry.
func (m *Manager) Drain(name string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		m.mu.Lock()
		stale := m.drained[name]
		if len(stale) == 0 {
			m.mu.Unlock()
			return 0
		}
		max := 0
		for _, e := range stale {
			r := int(atomic.LoadInt32(&e.refs))
			if r > max {
				max = r
			}
		}
		if max == 0 {
			// All superseded entries drained; finalize them.
			m.drained[name] = nil
			m.mu.Unlock()
			return 0
		}
		m.mu.Unlock()
		if time.Now().After(deadline) {
			return max
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Active returns a snapshot of registered agent names.
func (m *Manager) Active() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.active))
	for n := range m.active {
		out = append(out, n)
	}
	return out
}
