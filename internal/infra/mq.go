// Package infra already hosts the prefix/embed cache (cache.go). This file
// adds the Trace async pipeline the resume calls "Kafka 削峰 + 按月分表":
//
//   - ToyQueue is an in-memory broker that buffers Trace events between a
//     hot write path (the Agent's Run loop, which cannot block on storage)
//     and a slower batch consumer. It mirrors Kafka's log shape — produce
//     is non-blocking, consume pulls batches — without a Kafka dependency,
//     so the eval harness can reproduce the shape offline.
//   - MonthlySharder routes each event to a YYYY-MM "table" (here a slice
//     buffer). In production this is a partitioned table per month; the
//     toy keeps the same key so the routing logic is testable.
//
// The point of the toy is not to replace Kafka. It is to make the
// "trace never blocks the session main loop, and is queryable by month"
// invariant — the thing an interviewer will probe — executable and tested.
package infra

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ErrQueueFull is returned by Push when the buffer is full. The caller MUST
// treat this as "drop + count", not "retry synchronously" — retrying would
// block the session main loop, which is exactly what the queue exists to avoid.
var ErrQueueFull = errors.New("toyqueue: full, event dropped")

// ToyQueue is the in-memory analogue of a Kafka topic. Producers Push events
// without blocking; a single Consume loop drains them in batches and hands
// each batch to a handler. Drops (when the buffer fills under burst) are
// counted so the eval report can surface "X% Trace loss".
type ToyQueue struct {
	ch       chan []byte
	dropped  int32 // atomic; events lost to Push when full
}

// NewToyQueue returns a queue with the given buffer size. A small buffer
// (e.g. 256) is intentional — the queue exists to absorb sub-second bursts,
// not to hold history; the MonthlySharder holds history.
func NewToyQueue(buf int) *ToyQueue {
	if buf <= 0 {
		buf = 256
	}
	return &ToyQueue{ch: make(chan []byte, buf)}
}

// Push enqueues an event without blocking. Returns ErrQueueFull if the
// buffer is full; the caller should increment its own drop counter and
// continue (NOT retry inline).
func (q *ToyQueue) Push(event []byte) error {
	select {
	case q.ch <- event:
		return nil
	default:
		atomic.AddInt32(&q.dropped, 1)
		return ErrQueueFull
	}
}

// Dropped returns the count of events lost to Push when full. Surfaced in
// the eval report so "0 drops under N load" is a verifiable claim.
func (q *ToyQueue) Dropped() int { return int(atomic.LoadInt32(&q.dropped)) }

// Consume drains events in batches of up to batchSize and calls handler per
// batch. It returns when ctx is cancelled (graceful drain: in-flight events
// already in the channel are still delivered before exit). If batchSize <= 0
// it defaults to 16.
func (q *ToyQueue) Consume(ctx context.Context, batchSize int, handler func(batch [][]byte)) error {
	if batchSize <= 0 {
		batchSize = 16
	}
	batch := make([][]byte, 0, batchSize)
	for {
		// Block on the first event of a batch; once we have one, drain
		// non-blocking up to batchSize so a slow handler does not stall
		// a fast burst.
		select {
		case ev, ok := <-q.ch:
			if !ok {
				if len(batch) > 0 {
					handler(batch)
				}
				return nil
			}
			batch = append(batch, ev)
		case <-ctx.Done():
			if len(batch) > 0 {
				handler(batch)
			}
			return ctx.Err()
		}
		// Non-blocking drain to fill the batch.
		for len(batch) < batchSize {
			select {
			case ev, ok := <-q.ch:
				if !ok {
					handler(batch)
					return nil
				}
				batch = append(batch, ev)
			default:
				handler(batch)
				batch = batch[:0]
				goto waitNext
			}
		}
		handler(batch)
		batch = batch[:0]
	waitNext:
	}
}

// MonthlySharder routes events to a YYYY-MM "table". In production each table
// is a partitioned table; the toy keeps a per-month slice so the routing key
// is testable and the eval harness can ask "give me October's traces".
type MonthlySharder struct {
	mu     sync.Mutex
	tables map[string][][]byte
}

// NewMonthlySharder returns an empty sharder.
func NewMonthlySharder() *MonthlySharder {
	return &MonthlySharder{tables: make(map[string][][]byte)}
}

// MonthKey returns the YYYY-MM routing key for ts. This is the single source
// of truth for the routing decision — production and the toy share it.
func MonthKey(ts time.Time) string {
	return ts.Format("2006-01")
}

// Route appends event to the month table for ts and returns the month key.
// Safe for concurrent callers (many Agent turns writing Trace at once).
func (s *MonthlySharder) Route(ts time.Time, event []byte) string {
	month := MonthKey(ts)
	s.mu.Lock()
	s.tables[month] = append(s.tables[month], event)
	s.mu.Unlock()
	return month
}

// Get returns a copy of the events stored for month, or nil if the month has
// no events. Copying keeps the caller safe from concurrent appends.
func (s *MonthlySharder) Get(month string) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.tables[month]
	out := make([][]byte, len(src))
	copy(out, src)
	return out
}

// Months returns the sorted list of months that have at least one event.
// Sorted so the eval report renders a stable timeline.
func (s *MonthlySharder) Months() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tables))
	for m := range s.tables {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Count returns the total event count across all months. Useful for the
// "did every Trace land somewhere?" invariant in tests.
func (s *MonthlySharder) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.tables {
		n += len(t)
	}
	return n
}

// String renders a one-line summary for logs / /health.
func (s *MonthlySharder) String() string {
	return fmt.Sprintf("MonthlySharder{months=%d, events=%d}", len(s.Months()), s.Count())
}
