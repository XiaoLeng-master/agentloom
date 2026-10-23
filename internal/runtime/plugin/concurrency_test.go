package plugin

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseIsIdempotent(t *testing.T) {
	l := NewLifecycle()
	p := &fakePlugin{name: "tool"}
	if err := l.Mount(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h, err := l.Acquire("tool")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.Release() }()
	}
	wg.Wait()
	if n := atomic.LoadInt32(h.refs); n != 0 {
		t.Fatalf("refs = %d", n)
	}
}

type gatedPlugin struct {
	fakePlugin
	ready   chan struct{}
	proceed chan struct{}
}

func (p *gatedPlugin) Init(ctx context.Context) error {
	close(p.ready)
	<-p.proceed
	return p.fakePlugin.Init(ctx)
}

func TestConcurrentReloadPreservesEveryVersion(t *testing.T) {
	l := NewLifecycle()
	ctx := context.Background()
	first := &fakePlugin{name: "tool"}
	if err := l.Mount(ctx, first); err != nil {
		t.Fatal(err)
	}
	a := &gatedPlugin{fakePlugin: fakePlugin{name: "tool"}, ready: make(chan struct{}), proceed: make(chan struct{})}
	b := &gatedPlugin{fakePlugin: fakePlugin{name: "tool"}, ready: make(chan struct{}), proceed: make(chan struct{})}
	done := make(chan error, 2)
	for _, p := range []*gatedPlugin{a, b} {
		go func(p *gatedPlugin) { _, err := l.Reload(ctx, p); done <- err }(p)
	}
	<-a.ready
	<-b.ready
	close(a.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	held, err := l.Acquire("tool")
	if err != nil {
		t.Fatal(err)
	}
	close(b.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := l.Version("tool"); got != 3 {
		t.Fatalf("version = %d", got)
	}
	if remaining := l.Dispose("tool", 0); remaining != 1 {
		t.Fatalf("remaining = %d", remaining)
	}
	if atomic.LoadInt32(&a.disposes) != 0 {
		t.Fatal("disposed acquired plugin")
	}
	held.Release()
	if l.Dispose("tool", time.Second) != 0 {
		t.Fatal("failed to drain")
	}
	if atomic.LoadInt32(&first.disposes) != 1 || atomic.LoadInt32(&a.disposes) != 1 {
		t.Fatal("superseded version leaked")
	}
	if atomic.LoadInt32(&b.disposes) != 0 {
		t.Fatal("disposed active version")
	}
}
