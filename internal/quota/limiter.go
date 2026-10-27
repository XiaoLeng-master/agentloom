// Package quota limits concurrent work with two semaphores: one global, and
// one per key (an Agent name or a tenant). Callers that arrive over either
// limit wait in a queue until a slot frees or their context ends. A context
// cancellation or deadline while waiting is counted as a rejection.
package quota

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrRejected is returned when a caller stops waiting because its context
// was cancelled or its deadline was exceeded. It wraps the context error.
var ErrRejected = errors.New("quota: concurrency rejected")

// Stats is a point-in-time view of a Limiter. Inflight and Queued are gauges;
// Rejected and Admitted are cumulative. Peaks are the high-water marks since
// the limiter was created.
type Stats struct {
	Inflight     int   `json:"inflight"`
	Queued       int   `json:"queued"`
	Rejected     int64 `json:"rejected"`
	PeakInflight int   `json:"peak_inflight"`
	PeakQueued   int   `json:"peak_queued"`
	Admitted     int64 `json:"admitted"`
	GlobalLimit  int   `json:"global_limit"`
	PerKeyLimit  int   `json:"per_key_limit"`
}

// Limiter is a two-level concurrency semaphore. The zero value is not usable;
// construct one with New.
type Limiter struct {
	globalMax int
	perMax    int

	mu           sync.Mutex
	inflight     int
	queued       int
	per          map[string]int
	waiters      []*ticket
	rejected     int64
	peakInflight int
	peakQueued   int
	admitted     int64
	onChange     func()
}

type ticket struct {
	key   string
	ready chan struct{}
}

// New returns a limiter. Non-positive limits are clamped to 1 so a misconfigured
// caller cannot deadlock the queue.
func New(global, perKey int) *Limiter {
	if global < 1 {
		global = 1
	}
	if perKey < 1 {
		perKey = 1
	}
	return &Limiter{
		globalMax: global,
		perMax:    perKey,
		per:       map[string]int{},
	}
}

// SetOnChange registers a callback invoked after inflight, queued, or rejected
// changes. The callback runs without the limiter lock and may call Stats.
// It is intended for a parent that aggregates this limiter with another queue.
func (l *Limiter) SetOnChange(fn func()) {
	l.mu.Lock()
	l.onChange = fn
	l.mu.Unlock()
}

// Acquire waits until both the global semaphore and the per-key semaphore have
// a free slot. The returned release function must be called exactly once when
// the work finishes; extra calls are ignored. If ctx ends while the caller is
// queued, Acquire returns ErrRejected and increments the rejection counter.
func (l *Limiter) Acquire(ctx context.Context, key string) (func(), error) {
	if key == "" {
		key = "_"
	}
	if err := ctx.Err(); err != nil {
		l.mu.Lock()
		l.rejected++
		l.mu.Unlock()
		l.notify()
		return nil, fmt.Errorf("%w: %w", ErrRejected, err)
	}

	l.mu.Lock()
	if len(l.waiters) == 0 && l.canAdmit(key) {
		l.admitLocked(key)
		l.mu.Unlock()
		l.notify()
		return l.releaser(key), nil
	}
	t := &ticket{key: key, ready: make(chan struct{})}
	l.waiters = append(l.waiters, t)
	l.queued++
	if l.queued > l.peakQueued {
		l.peakQueued = l.queued
	}
	// A later waiter may be eligible even when an earlier key is saturated.
	l.grantLocked()
	l.mu.Unlock()
	l.notify()

	select {
	case <-ctx.Done():
		l.mu.Lock()
		if l.dequeue(t) {
			l.queued--
			l.rejected++
			l.mu.Unlock()
			l.notify()
			return nil, fmt.Errorf("%w: %w", ErrRejected, ctx.Err())
		}
		// Lost the race with grant: the slot is already ours. Drop it and
		// still count a rejection — the caller did not get to run the work.
		l.mu.Unlock()
		<-t.ready
		l.releaser(key)()
		l.mu.Lock()
		l.rejected++
		l.mu.Unlock()
		l.notify()
		return nil, fmt.Errorf("%w: %w", ErrRejected, ctx.Err())
	case <-t.ready:
		if err := ctx.Err(); err != nil {
			l.releaser(key)()
			l.mu.Lock()
			l.rejected++
			l.mu.Unlock()
			l.notify()
			return nil, fmt.Errorf("%w: %w", ErrRejected, err)
		}
		return l.releaser(key), nil
	}
}

// Reject counts one rejection that never reached Acquire, for example a task
// abandoned while it was still sitting in a worker-pool backlog.
func (l *Limiter) Reject() {
	l.mu.Lock()
	l.rejected++
	l.mu.Unlock()
	l.notify()
}

// Stats returns the current gauges and cumulative counters.
func (l *Limiter) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.statsLocked()
}

func (l *Limiter) statsLocked() Stats {
	return Stats{
		Inflight:     l.inflight,
		Queued:       l.queued,
		Rejected:     l.rejected,
		PeakInflight: l.peakInflight,
		PeakQueued:   l.peakQueued,
		Admitted:     l.admitted,
		GlobalLimit:  l.globalMax,
		PerKeyLimit:  l.perMax,
	}
}

func (l *Limiter) canAdmit(key string) bool {
	return l.inflight < l.globalMax && l.per[key] < l.perMax
}

func (l *Limiter) admitLocked(key string) {
	l.inflight++
	l.per[key]++
	l.admitted++
	if l.inflight > l.peakInflight {
		l.peakInflight = l.inflight
	}
}

// grantLocked admits the earliest waiters that fit. A saturated key does not
// block a later waiter whose own key and the global semaphore both have room.
func (l *Limiter) grantLocked() {
	for i := 0; i < len(l.waiters); {
		if l.inflight >= l.globalMax {
			return
		}
		t := l.waiters[i]
		if l.per[t.key] >= l.perMax {
			i++
			continue
		}
		l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
		l.queued--
		l.admitLocked(t.key)
		close(t.ready)
	}
}

func (l *Limiter) dequeue(t *ticket) bool {
	for i, w := range l.waiters {
		if w == t {
			l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
			return true
		}
	}
	return false
}

func (l *Limiter) releaser(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.inflight--
			l.per[key]--
			if l.per[key] <= 0 {
				delete(l.per, key)
			}
			l.grantLocked()
			l.mu.Unlock()
			l.notify()
		})
	}
}

func (l *Limiter) notify() {
	l.mu.Lock()
	fn := l.onChange
	l.mu.Unlock()
	if fn != nil {
		fn()
	}
}
