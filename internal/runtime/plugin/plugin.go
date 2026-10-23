// Package plugin implements the IoC plugin lifecycle that DSH-style Agent
// runtimes use to keep planner / memory / tool / evaluator swappable at runtime.
//
// The contract is deliberately narrow: a Plugin owns only its own resources
// (a client connection, a loaded model, an evaluator registry). It never
// receives an Agent reference, so mounting or disposing a Plugin cannot reset
// the Agent's conversation state, which remains independent of plugin lifecycle.
//
// Concurrency follows the same pattern as internal/config: a Handle holds a
// refcount on the active version; Reload supersedes the old version but leaves
// it alive until its in-flight refs drain; Dispose waits for that drain.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Kind classifies a Plugin so the runtime can route by capability rather than
// by name. The four kinds are planner, memory, tool, and evaluator.
type Kind string

const (
	KindPlanner   Kind = "planner"
	KindMemory    Kind = "memory"
	KindTool      Kind = "tool"
	KindEvaluator Kind = "evaluator"
)

// String returns the kind as a human label for logs and the /health surface.
func (k Kind) String() string { return string(k) }

// Plugin is the hot-pluggable contract. Implementations own their resources
// only — Init opens them, Dispose closes them — and must be safe to call from
// a goroutine distinct from the Agent's Run loop.
type Plugin interface {
	// Name is the unique handle; two plugins of the same Kind must still differ
	// by Name (e.g. "planner:default", "tool:rag-search").
	Name() string
	// Kind returns one of the four capability buckets.
	Kind() Kind
	// Init prepares the plugin's own resources (dial a client, load weights,
	// build an index). It is called once at Mount time. Returning an error
	// aborts the Mount and leaves the lifecycle untouched.
	Init(ctx context.Context) error
	// Dispose releases the plugin's own resources. It MUST NOT touch Agent
	// state — the Agent's conversation memory lives on the Agent struct, not
	// on the plugin. Dispose is idempotent.
	Dispose() error
}

// ErrUnknownPlugin is returned by Acquire when no plugin is mounted for name.
var ErrUnknownPlugin = errors.New("plugin: unknown")

// ErrDuplicateMount is returned by Mount when name is already mounted.
// Use Reload to swap, not Mount.
var ErrDuplicateMount = errors.New("plugin: already mounted, use Reload")

// Handle is a refcounted reference to the active version of a plugin. Acquire
// increments the refcount; Release decrements it. A Reload supersedes the old
// version, but the old version stays alive until its refcount drains — that is
// the zero-interrupt swap the harness relies on to ship an optimization
// without breaking in-flight turns.
type Handle struct {
	plugin   Plugin
	refs     *int32
	stale    *atomic.Bool
	released atomic.Bool
}

// Plugin returns the plugin this handle was acquired against.
func (h *Handle) Plugin() Plugin { return h.plugin }

// IsStale reports whether a newer version has superseded this handle. A stale
// handle still serves the old plugin correctly — callers use this only to
// decide whether to re-Acquire for the next turn.
func (h *Handle) IsStale() bool {
	if h.stale == nil {
		return false
	}
	return h.stale.Load()
}

// Release decrements the refcount. Always defer it once per Acquire.
func (h *Handle) Release() {
	if h.refs != nil && h.released.CompareAndSwap(false, true) {
		atomic.AddInt32(h.refs, -1)
	}
}

type entry struct {
	plugin  Plugin
	refs    int32
	stale   atomic.Bool
	version int
}

// Lifecycle owns the active plugin per name and performs mount / reload /
// dispose. It is the IoC container the Agent runtime consults when it needs a
// planner, a memory backend, a tool, or an evaluator — and when the config
// watcher says a new version of one of those is available.
type Lifecycle struct {
	mu       sync.RWMutex
	active   map[string]*entry
	draining map[string][]*entry
}

// NewLifecycle returns an empty Lifecycle.
func NewLifecycle() *Lifecycle {
	return &Lifecycle{
		active:   make(map[string]*entry),
		draining: make(map[string][]*entry),
	}
}

// Mount installs p as the initial version of p.Name(). It fails if the name is
// already mounted (use Reload to swap). Init runs under the lock so a failed
// Init cannot leave a half-registered entry.
func (l *Lifecycle) Mount(ctx context.Context, p Plugin) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	name := p.Name()
	if _, ok := l.active[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateMount, name)
	}
	if err := p.Init(ctx); err != nil {
		return fmt.Errorf("plugin mount %q: init: %w", name, err)
	}
	l.active[name] = &entry{plugin: p, version: 1}
	return nil
}

// Acquire returns a refcounted handle to the active plugin for name. The
// refcount is incremented so a concurrent Reload will drain the old version
// instead of tearing it down under a running turn.
func (l *Lifecycle) Acquire(name string) (*Handle, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.active[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownPlugin, name)
	}
	atomic.AddInt32(&e.refs, 1)
	return &Handle{plugin: e.plugin, refs: &e.refs, stale: &e.stale}, nil
}

// Reload installs newP as the new version of newP.Name() and supersedes the
// previous one. New Acquires hit the new version; in-flight handles against
// the old version keep running. Returns the in-flight refcount of the
// superseded version (0 if there was no prior version or it had no users).
//
// Reload fails if newP was never mounted — the harness uses this to refuse a
// config change for a plugin the operator typo'd. Init runs before the swap
// so a failing Init cannot unmount the working plugin.
func (l *Lifecycle) Reload(ctx context.Context, newP Plugin) (inFlight int, err error) {
	name := newP.Name()
	l.mu.Lock()
	old, ok := l.active[name]
	l.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: %s (mount first)", ErrUnknownPlugin, name)
	}
	if err := newP.Init(ctx); err != nil {
		return 0, fmt.Errorf("plugin reload %q: init: %w", name, err)
	}
	l.mu.Lock()
	old, ok = l.active[name]
	if !ok {
		l.mu.Unlock()
		_ = newP.Dispose()
		return 0, fmt.Errorf("%w: %s (unmounted during init)", ErrUnknownPlugin, name)
	}
	version := 1
	if ok {
		version = old.version + 1
	}
	e := &entry{plugin: newP, version: version}
	l.active[name] = e
	if ok {
		l.draining[name] = append(l.draining[name], old)
	}
	if ok {
		old.stale.Store(true)
		l.mu.Unlock()
		return int(atomic.LoadInt32(&old.refs)), nil
	}
	l.mu.Unlock()
	return 0, nil
}

// Dispose waits until every superseded version of name has zero in-flight refs
// or the timeout elapses, then finalizes them by calling Dispose on each.
// Returns the max remaining refcount across stale versions (0 = clean drain).
//
// Dispose does NOT remove the active version — that stays mounted for the next
// turn. To fully unmount, call Unmount.
func (l *Lifecycle) Dispose(name string, timeout time.Duration) (remaining int) {
	deadline := time.Now().Add(timeout)
	for {
		l.mu.Lock()
		stale := l.draining[name]
		if len(stale) == 0 {
			l.mu.Unlock()
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
			toFinalize := l.draining[name]
			l.draining[name] = nil
			l.mu.Unlock()
			for _, e := range toFinalize {
				_ = e.plugin.Dispose()
			}
			return 0
		}
		l.mu.Unlock()
		if time.Now().After(deadline) {
			return max
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Unmount removes the active version of name immediately. It refuses if there
// are in-flight refs on the active version (call Dispose first for stale
// versions, then DrainActive for the active one). Returns the in-flight count
// if it refused, 0 on success.
func (l *Lifecycle) Unmount(name string) (inFlight int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.active[name]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnknownPlugin, name)
	}
	refs := int(atomic.LoadInt32(&e.refs))
	if refs > 0 {
		return refs, fmt.Errorf("plugin unmount %q: %d in-flight refs", name, refs)
	}
	_ = e.plugin.Dispose()
	delete(l.active, name)
	return 0, nil
}

// Names returns the mounted plugin names, sorted for deterministic output
// (used by /health and the registry snapshot in tests).
func (l *Lifecycle) Names() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.active))
	for n := range l.active {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ByKind returns the mounted plugin names grouped by Kind. The snapshot is
// sorted within each group. This is the surface the orchestrator uses to pick
// the active planner / memory / tool / evaluator set for a turn.
func (l *Lifecycle) ByKind() map[Kind][]string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[Kind][]string, 4)
	for n, e := range l.active {
		k := e.plugin.Kind()
		out[k] = append(out[k], n)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// Version returns the current version counter for name, or 0 if unmounted.
// Useful for the harness to detect that a Reload has landed before retrying.
func (l *Lifecycle) Version(name string) int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if e, ok := l.active[name]; ok {
		return e.version
	}
	return 0
}
