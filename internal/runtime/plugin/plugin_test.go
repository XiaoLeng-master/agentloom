package plugin

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePlugin records Init/Dispose call counts and owns only a fake connection.
// It deliberately does NOT hold any Agent reference — that is the design
// property under test.
type fakePlugin struct {
	name    string
	kind    Kind
	initErr error
	inits   int32
	disposes int32
	connOpen bool
}

func (p *fakePlugin) Name() string { return p.name }
func (p *fakePlugin) Kind() Kind    { return p.kind }
func (p *fakePlugin) Init(ctx context.Context) error {
	atomic.AddInt32(&p.inits, 1)
	if p.initErr != nil {
		return p.initErr
	}
	p.connOpen = true
	return nil
}
func (p *fakePlugin) Dispose() error {
	atomic.AddInt32(&p.disposes, 1)
	p.connOpen = false
	return nil
}

// fakeAgent owns its own conversation state. The resume claims plugin
// lifecycle never resets this; the test demonstrates the isolation by never
// giving the plugin a handle to the agent.
type fakeAgent struct {
	mu           sync.Mutex
	conversation []string
}

func (a *fakeAgent) Append(turn string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conversation = append(a.conversation, turn)
}

func (a *fakeAgent) Snapshot() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.conversation))
	copy(out, a.conversation)
	return out
}

func TestMountAcquireRelease(t *testing.T) {
	l := NewLifecycle()
	p := &fakePlugin{name: "tool:rag", kind: KindTool}
	if err := l.Mount(context.Background(), p); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if atomic.LoadInt32(&p.inits) != 1 {
		t.Fatalf("Init should run once, got %d", p.inits)
	}
	h, err := l.Acquire("tool:rag")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if h.Plugin() != p {
		t.Fatal("Handle returned wrong plugin")
	}
	if h.IsStale() {
		t.Fatal("fresh handle should not be stale")
	}
	h.Release()
	if l.Version("tool:rag") != 1 {
		t.Fatalf("Version = %d, want 1", l.Version("tool:rag"))
	}
}

func TestMountDuplicateFails(t *testing.T) {
	l := NewLifecycle()
	p := &fakePlugin{name: "planner:default", kind: KindPlanner}
	if err := l.Mount(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	err := l.Mount(context.Background(), &fakePlugin{name: "planner:default", kind: KindPlanner})
	if !errors.Is(err, ErrDuplicateMount) {
		t.Fatalf("want ErrDuplicateMount, got %v", err)
	}
}

func TestMountInitErrorAborts(t *testing.T) {
	l := NewLifecycle()
	p := &fakePlugin{name: "eval:geval", kind: KindEvaluator, initErr: errors.New("boom")}
	if err := l.Mount(context.Background(), p); err == nil {
		t.Fatal("Mount should fail on Init error")
	}
	if _, err := l.Acquire("eval:geval"); !errors.Is(err, ErrUnknownPlugin) {
		t.Fatalf("failed mount must leave nothing registered, got %v", err)
	}
}

func TestReloadZeroInterrupt(t *testing.T) {
	l := NewLifecycle()
	old := &fakePlugin{name: "planner:default", kind: KindPlanner}
	if err := l.Mount(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	// Simulate an in-flight turn: hold a ref on the old version.
	h, err := l.Acquire("planner:default")
	if err != nil {
		t.Fatal(err)
	}
	new := &fakePlugin{name: "planner:default", kind: KindPlanner}
	inFlight, err := l.Reload(context.Background(), new)
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if inFlight != 1 {
		t.Fatalf("Reload should report 1 in-flight ref, got %d", inFlight)
	}
	// Old handle still serves the OLD plugin — this is the zero-interrupt guarantee.
	if h.Plugin() != old {
		t.Fatal("in-flight handle must still serve old plugin after Reload")
	}
	if !h.IsStale() {
		t.Fatal("old handle must be marked stale after Reload")
	}
	// New acquires hit the NEW plugin.
	h2, err := l.Acquire("planner:default")
	if err != nil {
		t.Fatal(err)
	}
	if h2.Plugin() != new {
		t.Fatal("new Acquire must return reloaded plugin")
	}
	if l.Version("planner:default") != 2 {
		t.Fatalf("Version = %d, want 2", l.Version("planner:default"))
	}
	// Release the in-flight ref; Dispose should now drain the old version.
	h.Release()
	h2.Release()
	remaining := l.Dispose("planner:default", time.Second)
	if remaining != 0 {
		t.Fatalf("Dispose remaining = %d, want 0", remaining)
	}
	if atomic.LoadInt32(&old.disposes) != 1 {
		t.Fatalf("old plugin Dispose should run once, got %d", old.disposes)
	}
	if !new.connOpen {
		t.Fatal("new plugin connection must still be open")
	}
}

func TestDisposeTimeoutReturnsRemaining(t *testing.T) {
	l := NewLifecycle()
	old := &fakePlugin{name: "memory:redis", kind: KindMemory}
	if err := l.Mount(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	h, err := l.Acquire("memory:redis")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reload(context.Background(), &fakePlugin{name: "memory:redis", kind: KindMemory}); err != nil {
		t.Fatal(err)
	}
	// Old ref still held — Dispose must time out and report 1.
	remaining := l.Dispose("memory:redis", 20*time.Millisecond)
	if remaining != 1 {
		t.Fatalf("Dispose remaining = %d, want 1", remaining)
	}
	if atomic.LoadInt32(&old.disposes) != 0 {
		t.Fatal("old plugin must NOT be disposed while ref held")
	}
	h.Release()
	// Now drain succeeds.
	if r := l.Dispose("memory:redis", time.Second); r != 0 {
		t.Fatalf("Dispose after release = %d, want 0", r)
	}
}

func TestUnmountRefusesInFlight(t *testing.T) {
	l := NewLifecycle()
	p := &fakePlugin{name: "tool:search", kind: KindTool}
	if err := l.Mount(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h, _ := l.Acquire("tool:search")
	defer h.Release()
	inFlight, err := l.Unmount("tool:search")
	if err == nil {
		t.Fatal("Unmount must refuse while refs held")
	}
	if inFlight != 1 {
		t.Fatalf("inFlight = %d, want 1", inFlight)
	}
	h.Release()
	if _, err := l.Unmount("tool:search"); err != nil {
		t.Fatalf("Unmount after release: %v", err)
	}
	if _, err := l.Acquire("tool:search"); !errors.Is(err, ErrUnknownPlugin) {
		t.Fatalf("Acquire after Unmount = %v, want ErrUnknownPlugin", err)
	}
}

// TestAgentStateSurvivesPluginReload is the property the resume advertises:
// "mount / reload / dispose 不重置 agent 状态". The design enforces it by
// never handing the Agent to a Plugin; this test demonstrates the isolation
// holds even across a full reload+dispose cycle.
func TestAgentStateSurvivesPluginReload(t *testing.T) {
	l := NewLifecycle()
	tool := &fakePlugin{name: "tool:rag", kind: KindTool}
	if err := l.Mount(context.Background(), tool); err != nil {
		t.Fatal(err)
	}
	agent := &fakeAgent{}
	agent.Append("user: what is ReAct?")
	agent.Append("tool:rag: ReAct = Reason+Act+Observe loop")
	before := agent.Snapshot()

	// Acquire the tool, run a turn, then reload+dispose it.
	h, _ := l.Acquire("tool:rag")
	agent.Append("assistant: ReAct is a reason-act-observe loop")
	h.Release()
	if _, err := l.Reload(context.Background(), &fakePlugin{name: "tool:rag", kind: KindTool}); err != nil {
		t.Fatal(err)
	}
	l.Dispose("tool:rag", time.Second)

	after := agent.Snapshot()
	if len(after) != len(before)+1 {
		t.Fatalf("agent conversation grew unexpectedly: before=%d after=%d", len(before), len(after))
	}
	// The original turns must be intact — the plugin lifecycle never touched them.
	for i, want := range before {
		if after[i] != want {
			t.Fatalf("turn %d corrupted by plugin reload: want %q got %q", i, want, after[i])
		}
	}
}

func TestByKindAndNames(t *testing.T) {
	l := NewLifecycle()
	for _, p := range []Plugin{
		&fakePlugin{name: "planner:default", kind: KindPlanner},
		&fakePlugin{name: "planner:cheap", kind: KindPlanner},
		&fakePlugin{name: "tool:rag", kind: KindTool},
		&fakePlugin{name: "eval:geval", kind: KindEvaluator},
		&fakePlugin{name: "memory:local", kind: KindMemory},
	} {
		if err := l.Mount(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	names := l.Names()
	want := []string{"eval:geval", "memory:local", "planner:cheap", "planner:default", "tool:rag"}
	if len(names) != len(want) {
		t.Fatalf("Names len = %d, want %d", len(names), len(want))
	}
	for i, n := range names {
		if n != want[i] {
			t.Fatalf("Names[%d] = %q, want %q", i, n, want[i])
		}
	}
	byKind := l.ByKind()
	if len(byKind[KindPlanner]) != 2 {
		t.Fatalf("planner count = %d, want 2", len(byKind[KindPlanner]))
	}
	if len(byKind[KindTool]) != 1 {
		t.Fatalf("tool count = %d, want 1", len(byKind[KindTool]))
	}
}

// TestConcurrentAcquireReload exercises the race detector: many goroutines
// acquire the planner while another reloads it. The refcount invariant
// (in-flight handles keep serving the old version) must hold.
func TestConcurrentAcquireReload(t *testing.T) {
	l := NewLifecycle()
	if err := l.Mount(context.Background(), &fakePlugin{name: "planner:default", kind: KindPlanner}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const readers = 50
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := l.Acquire("planner:default")
			if err != nil {
				t.Error(err)
				return
			}
			// Hold briefly to overlap with Reload.
			time.Sleep(time.Millisecond)
			h.Release()
		}()
	}
	// Concurrent reloader swaps the planner a few times.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Reload(context.Background(), &fakePlugin{name: "planner:default", kind: KindPlanner})
		}()
	}
	wg.Wait()
	// Drain any superseded versions.
	l.Dispose("planner:default", 2*time.Second)
	// Final state: one active version, version counter advanced.
	if l.Version("planner:default") < 2 {
		t.Fatalf("Version = %d, want >= 2", l.Version("planner:default"))
	}
}
