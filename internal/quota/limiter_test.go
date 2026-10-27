package quota

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterGlobalCap(t *testing.T) {
	const limit = 2
	l := New(limit, 10)
	release := make(chan struct{})
	var inflight atomic.Int32
	var max atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := l.Acquire(context.Background(), "a")
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer rel()
			cur := inflight.Add(1)
			for {
				prev := max.Load()
				if cur <= prev || max.CompareAndSwap(prev, cur) {
					break
				}
			}
			<-release
			inflight.Add(-1)
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && inflight.Load() < limit {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := inflight.Load(); got > limit {
		t.Fatalf("inflight=%d exceeded global limit %d", got, limit)
	}
	if got := l.Stats().PeakInflight; got > limit {
		t.Fatalf("peak inflight=%d exceeded global limit %d", got, limit)
	}
	close(release)
	wg.Wait()
	if got := max.Load(); got > limit {
		t.Fatalf("observed max=%d exceeded global limit %d", got, limit)
	}
	st := l.Stats()
	if st.Inflight != 0 || st.Queued != 0 {
		t.Fatalf("after drain inflight=%d queued=%d", st.Inflight, st.Queued)
	}
	if st.Admitted != 6 {
		t.Fatalf("admitted=%d, want 6", st.Admitted)
	}
}

func TestLimiterPerKeyDoesNotBlockOtherKeys(t *testing.T) {
	l := New(4, 1)
	relA, err := l.Acquire(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	defer relA()

	stuck := make(chan struct{})
	go func() {
		rel, err := l.Acquire(context.Background(), "A")
		if err != nil {
			t.Errorf("second A: %v", err)
			close(stuck)
			return
		}
		rel()
		close(stuck)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && l.Stats().Queued < 1 {
		time.Sleep(2 * time.Millisecond)
	}
	if l.Stats().Queued < 1 {
		t.Fatal("second A should be queued behind the per-key limit")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	relB, err := l.Acquire(ctx, "B")
	if err != nil {
		t.Fatalf("B should run while A is saturated: %v", err)
	}
	relB()
	relA()
	select {
	case <-stuck:
	case <-time.After(time.Second):
		t.Fatal("second A did not finish after release")
	}
	st := l.Stats()
	if st.PeakInflight > 2 {
		t.Fatalf("peak inflight=%d, two keys with per-key 1 should overlap at most 2", st.PeakInflight)
	}
}

func TestLimiterContextTimeoutRejects(t *testing.T) {
	l := New(1, 1)
	rel, err := l.Acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = l.Acquire(ctx, "a")
	if !errors.Is(err, ErrRejected) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want rejected deadline, got %v", err)
	}
	st := l.Stats()
	if st.Rejected != 1 {
		t.Fatalf("rejected=%d, want 1", st.Rejected)
	}
	if st.Admitted != 1 {
		t.Fatalf("admitted=%d, want 1 (only the holder; the timed-out waiter is not admitted)", st.Admitted)
	}
	if st.Inflight != 1 {
		t.Fatalf("holder inflight=%d, want 1", st.Inflight)
	}
	rel()
	if l.Stats().Inflight != 0 {
		t.Fatal("release did not free the slot")
	}
}

func TestLimiterCancelBeforeAcquireRejects(t *testing.T) {
	l := New(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := l.Acquire(ctx, "a")
	if !errors.Is(err, ErrRejected) || !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if l.Stats().Rejected != 1 || l.Stats().Admitted != 0 {
		t.Fatalf("stats=%+v", l.Stats())
	}
}

func TestBacklogHandoffDoesNotDoubleCount(t *testing.T) {
	const n = 80
	l := New(1, 1)
	l.NoteBacklog(n)
	hold := make(chan struct{})
	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := l.AcquireFromBacklog(context.Background(), "a")
			if err != nil {
				failed.Add(1)
				return
			}
			<-hold
			rel()
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := l.Stats()
		if st.Backlog == 0 && st.Inflight+st.Queued == n {
			break
		}
		time.Sleep(time.Millisecond)
	}
	st := l.Stats()
	if st.PeakCombined != n {
		t.Fatalf("peak combined=%d, want %d (a handoff must not count a task twice) stats=%+v", st.PeakCombined, n, st)
	}
	if st.Backlog+st.Queued+st.Inflight != n {
		t.Fatalf("in-flight accounting=%+v, want sum %d", st, n)
	}
	close(hold)
	wg.Wait()
	end := l.Stats()
	if failed.Load() != 0 || end.Inflight != 0 || end.Queued != 0 || end.Backlog != 0 {
		t.Fatalf("end=%+v failed=%d", end, failed.Load())
	}
}

func TestLimiterReleaseIsIdempotent(t *testing.T) {
	l := New(1, 1)
	rel, err := l.Acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel()
	if l.Stats().Inflight != 0 {
		t.Fatalf("double release changed inflight: %+v", l.Stats())
	}
	rel2, err := l.Acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	rel2()
}
