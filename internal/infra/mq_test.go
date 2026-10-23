package infra

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPushPopBasic(t *testing.T) {
	q := NewToyQueue(8)
	for i := 0; i < 5; i++ {
		if err := q.Push([]byte(fmt.Sprintf("ev-%d", i))); err != nil {
			t.Fatalf("Push %d: %v", i, err)
		}
	}
	var got [][]byte
	done := make(chan struct{})
	go func() {
		q.Consume(context.Background(), 2, func(batch [][]byte) {
			got = append(got, batch...)
			if len(got) >= 5 {
				close(done)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Consume timed out")
	}
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	for i, ev := range got {
		want := fmt.Sprintf("ev-%d", i)
		if string(ev) != want {
			t.Fatalf("got[%d] = %q, want %q", i, ev, want)
		}
	}
	if q.Dropped() != 0 {
		t.Fatalf("dropped = %d, want 0", q.Dropped())
	}
}

func TestQueueFullDrops(t *testing.T) {
	q := NewToyQueue(2)
	if err := q.Push([]byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := q.Push([]byte("b")); err != nil {
		t.Fatal(err)
	}
	// Third push must drop, not block.
	if err := q.Push([]byte("c")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third Push = %v, want ErrQueueFull", err)
	}
	if q.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", q.Dropped())
	}
}

func TestConsumeRespectsContext(t *testing.T) {
	q := NewToyQueue(8)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan [][]byte, 10)
	go func() {
		q.Consume(ctx, 4, func(batch [][]byte) {
			got <- batch
		})
		close(got)
	}()
	q.Push([]byte("x"))
	cancel() // graceful drain: x should still arrive
	select {
	case batch := <-got:
		if len(batch) != 1 || string(batch[0]) != "x" {
			t.Fatalf("batch = %v, want [x]", batch)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight event not delivered after cancel")
	}
}

func TestCloseDrainsAcceptedEvents(t *testing.T) {
	q := NewToyQueue(128)
	var producers sync.WaitGroup
	for i := 0; i < 64; i++ {
		producers.Add(1)
		go func() { defer producers.Done(); _ = q.Push([]byte("trace")) }()
	}
	producers.Wait()
	q.Close()
	q.Close()
	if err := q.Push([]byte("late")); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("late Push: %v", err)
	}
	var received int
	if err := q.Consume(context.Background(), 7, func(batch [][]byte) { received += len(batch) }); err != nil {
		t.Fatal(err)
	}
	if received != 64 {
		t.Fatalf("received %d/64", received)
	}
}

func TestConcurrentCloseAndPush(t *testing.T) {
	q := NewToyQueue(128)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = q.Push([]byte("x")) }()
	}
	q.Close()
	wg.Wait()
	var received int
	if err := q.Consume(context.Background(), 8, func(batch [][]byte) { received += len(batch) }); err != nil {
		t.Fatal(err)
	}
	if received > 100 {
		t.Fatalf("received %d", received)
	}
}

func TestMonthlySharderRouting(t *testing.T) {
	s := NewMonthlySharder()
	oct1 := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	oct2 := time.Date(2026, 10, 31, 23, 0, 0, 0, time.UTC)
	nov1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	s.Route(oct1, []byte("oct-a"))
	s.Route(oct2, []byte("oct-b"))
	s.Route(nov1, []byte("nov-a"))

	if key := MonthKey(oct1); key != "2026-10" {
		t.Fatalf("MonthKey = %q, want 2026-10", key)
	}
	oct := s.Get("2026-10")
	if len(oct) != 2 {
		t.Fatalf("October events = %d, want 2", len(oct))
	}
	nov := s.Get("2026-11")
	if len(nov) != 1 {
		t.Fatalf("November events = %d, want 1", len(nov))
	}
	months := s.Months()
	if len(months) != 2 || months[0] != "2026-10" || months[1] != "2026-11" {
		t.Fatalf("Months = %v, want [2026-10 2026-11]", months)
	}
	if s.Count() != 3 {
		t.Fatalf("Count = %d, want 3", s.Count())
	}
}

// TestQueueBurstThenDrain exercises the "burst absorb then slow drain" shape:
// push a burst that fits, then a single consumer drains it. This is the
// exact load pattern the session main loop hits Trace at.
func TestQueueBurstThenDrain(t *testing.T) {
	q := NewToyQueue(64)
	const burst = 50
	for i := 0; i < burst; i++ {
		if err := q.Push([]byte("e")); err != nil {
			t.Fatalf("Push %d dropped: %v", i, err)
		}
	}
	var received int32
	done := make(chan struct{})
	go func() {
		q.Consume(context.Background(), 8, func(batch [][]byte) {
			atomic.AddInt32(&received, int32(len(batch)))
			if atomic.LoadInt32(&received) >= int32(burst) {
				close(done)
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("drain timed out, received %d/%d", received, burst)
	}
	if int(received) != burst {
		t.Fatalf("received = %d, want %d", received, burst)
	}
}

// TestConcurrentProducers is the race-detector target: many goroutines push
// Trace events while a consumer drains. The queue must not race and must not
// lose events that fit in the buffer.
func TestConcurrentProducers(t *testing.T) {
	q := NewToyQueue(512)
	const producers = 16
	const per = 32
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				_ = q.Push([]byte("trace"))
			}
		}()
	}
	var received int32
	done := make(chan struct{})
	go func() {
		q.Consume(context.Background(), 16, func(batch [][]byte) {
			atomic.AddInt32(&received, int32(len(batch)))
			if atomic.LoadInt32(&received) >= int32(producers*per) {
				close(done)
				return
			}
		})
	}()
	wg.Wait()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("drain timeout, received %d/%d", received, producers*per)
	}
	// All events must land (buffer is large enough for the burst).
	if q.Dropped() != 0 {
		t.Fatalf("dropped = %d, want 0 under adequate buffer", q.Dropped())
	}
}
