package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/quota"
)

func TestConcurrencyAdmissionPerTenantAndGlobal(t *testing.T) {
	a := NewConcurrencyAdmission(2, 1)
	release := make(chan struct{})
	var inflight atomic.Int32
	errCh := make(chan error, 4)
	start := func(tenant string) {
		go func() {
			rel, err := a.Enter(context.Background(), Request{Tenant: tenant})
			if err != nil {
				errCh <- err
				return
			}
			cur := inflight.Add(1)
			if cur > 2 {
				errCh <- errors.New("global cap exceeded")
			}
			<-release
			inflight.Add(-1)
			rel()
			errCh <- nil
		}()
	}
	start("A")
	start("A")
	start("B")
	start("C")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && inflight.Load() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := inflight.Load(); got > 2 {
		t.Fatalf("inflight=%d exceeded global 2", got)
	}
	// Tenant A is capped at 1, so the second A stays queued while B or C runs.
	st := a.Stats()
	if st.Queued < 1 {
		t.Fatalf("queued=%d, the second A (or a third distinct tenant) should wait", st.Queued)
	}
	if st.PeakInflight > 2 {
		t.Fatalf("peak=%d", st.PeakInflight)
	}
	close(release)
	for i := 0; i < 4; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrencyAdmissionUsesAgentWhenTenantEmpty(t *testing.T) {
	a := NewConcurrencyAdmission(2, 1)
	rel, err := a.Enter(context.Background(), Request{AgentName: "planner"})
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = a.Enter(ctx, Request{AgentName: "planner"})
	if !errors.Is(err, quota.ErrRejected) {
		t.Fatalf("same agent should share a key, got %v", err)
	}
	if a.Stats().Rejected != 1 {
		t.Fatalf("rejected=%d", a.Stats().Rejected)
	}
}

func TestConcurrencyAdmissionTimeoutRejects(t *testing.T) {
	a := NewConcurrencyAdmission(1, 1)
	rel, err := a.Enter(context.Background(), Request{Tenant: "local"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = a.Enter(ctx, Request{Tenant: "local"})
	if !errors.Is(err, quota.ErrRejected) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if a.Stats().Rejected != 1 {
		t.Fatalf("stats=%+v", a.Stats())
	}
	rel()
	rel2, err := a.Enter(context.Background(), Request{Tenant: "local"})
	if err != nil {
		t.Fatal(err)
	}
	rel2()
}
