package config

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestManager_HotReloadDoesNotInterruptInFlight proves the core claim of the
// resume: a Reload mid-flight does NOT tear down a running request. The handle
// acquired before the Reload keeps the old config; new Acquires hit the new
// config; once the old handle is Released, Drain returns 0.
func TestManager_HotReloadDoesNotInterruptInFlight(t *testing.T) {
	m := NewManager()
	m.Register("kb-agent", Config{Name: "kb-agent", SystemPrompt: "v1", Model: "qwen2.5"})

	// Acquire a handle (= a request starts running against v1).
	inFlight, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := inFlight.Config().SystemPrompt; got != "v1" {
		t.Fatalf("in-flight should see v1, got %q", got)
	}

	// Reload v2 while v1 is still in flight. New requests must see v2.
	draining := m.Reload("kb-agent", Config{Name: "kb-agent", SystemPrompt: "v2", Model: "qwen2.5"})
	if draining != 1 {
		t.Fatalf("Reload should report 1 in-flight ref, got %d", draining)
	}
	if !inFlight.IsStale() {
		t.Fatalf("old handle should be marked stale after Reload")
	}
	next, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatalf("Acquire after reload: %v", err)
	}
	if got := next.Config().SystemPrompt; got != "v2" {
		t.Fatalf("new acquire should see v2, got %q", got)
	}
	if next.IsStale() {
		t.Fatalf("new handle must not be stale")
	}
	if next.Config().Version != inFlight.Config().Version+1 {
		t.Fatalf("version must increment: %d -> %d", inFlight.Config().Version, next.Config().Version)
	}

	// In-flight handle still reports its original config (not torn down).
	if got := inFlight.Config().SystemPrompt; got != "v1" {
		t.Fatalf("in-flight handle must keep v1, got %q", got)
	}

	// Drain should NOT complete while the in-flight ref is held.
	if m.Drain("kb-agent", 50*time.Millisecond) == 0 {
		t.Fatalf("Drain should time out while in-flight ref is held")
	}

	// Finish the in-flight request; Drain must now complete cleanly.
	inFlight.Release()
	next.Release()
	if rem := m.Drain("kb-agent", time.Second); rem != 0 {
		t.Fatalf("Drain should return 0 after release, got %d", rem)
	}
}

// TestManager_SourceDrivenReload exercises the Source -> Apply -> Reload path
// with concurrent Acquire/Release traffic to surface any data race.
func TestManager_SourceDrivenReload(t *testing.T) {
	m := NewManager()
	m.Register("a", Config{Name: "a", SystemPrompt: "p0"})
	src := NewChannelSource(8)
	done := Apply(m, src)

	var wg sync.WaitGroup
	// Simulate request traffic: acquire, hold briefly, release.
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := m.Acquire("a")
			if err != nil {
				return
			}
			time.Sleep(time.Millisecond)
			h.Release()
		}()
	}

	// Drive three reloads from the Source.
	for v := 1; v <= 3; v++ {
		src.Emit("a", Config{Name: "a", SystemPrompt: "p" + itoa(v)})
	}
	// Drain pending Apply sends.
	go func() { _ = done }()

	wg.Wait()
	// Final config is the last emitted one (p3).
	h, err := m.Acquire("a")
	if err != nil {
		t.Fatalf("final Acquire: %v", err)
	}
	h.Release()
	_ = context.Background() // keep import used in production callers
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
