package orchestrator

import (
	"context"
	"fmt"
	"sync"

	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/quota"
)

const (
	// DefaultWorkers is the per-turn executor goroutine cap.
	DefaultWorkers = 4
	// DefaultGlobalLimit caps in-flight executor calls across every Run
	// sharing the orchestrator.
	DefaultGlobalLimit = 8
	// DefaultPerAgentLimit caps in-flight calls to a single executor Agent name.
	DefaultPerAgentLimit = 4
)

// ConcurrencyConfig sizes the worker pool and the two-level quota.
// A value <= 0 selects the corresponding default. Limits are concurrency
// slots, not a request-rate window: callers wait in a queue instead of being
// rejected immediately, and a cancelled or timed-out wait is counted.
type ConcurrencyConfig struct {
	Workers       int
	GlobalLimit   int
	PerAgentLimit int
}

// ConcurrencyStats exposes the worker-pool backlog together with the shared
// quota gauges. Queued is pool backlog plus callers blocked inside the
// semaphore. Rejected counts waits abandoned because ctx ended, including
// tasks that never left the pool backlog.
type ConcurrencyStats struct {
	Inflight        int   `json:"inflight"`
	Queued          int   `json:"queued"`
	Rejected        int64 `json:"rejected"`
	PeakInflight    int   `json:"peak_inflight"`
	PeakQueued      int   `json:"peak_queued"`
	QuotaPeakQueued int   `json:"quota_peak_queued"`
	PoolQueued      int   `json:"pool_queued"`
	QuotaQueued     int   `json:"quota_queued"`
	Admitted        int64 `json:"admitted"`
	Workers         int   `json:"workers"`
	GlobalLimit     int   `json:"global_limit"`
	PerAgentLimit   int   `json:"per_agent_limit"`
}

// SetConcurrency installs the worker-pool size and a fresh two-level limiter.
// Call it before Run. In-flight acquires keep the previous limiter.
func (o *Orchestrator) SetConcurrency(cfg ConcurrencyConfig) {
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	if cfg.GlobalLimit <= 0 {
		cfg.GlobalLimit = DefaultGlobalLimit
	}
	if cfg.PerAgentLimit <= 0 {
		cfg.PerAgentLimit = DefaultPerAgentLimit
	}
	lim := quota.New(cfg.GlobalLimit, cfg.PerAgentLimit)
	lim.SetOnChange(func() { o.recordQueuePeak() })
	o.concMu.Lock()
	o.workers = cfg.Workers
	o.globalLimit = cfg.GlobalLimit
	o.perAgentLimit = cfg.PerAgentLimit
	o.limiter = lim
	o.concMu.Unlock()
}

// ConcurrencyStats returns the current concurrency, queue depth, and rejection
// count for this orchestrator.
func (o *Orchestrator) ConcurrencyStats() ConcurrencyStats {
	o.concMu.RLock()
	lim := o.limiter
	workers := o.workers
	global := o.globalLimit
	per := o.perAgentLimit
	o.concMu.RUnlock()
	st := quota.Stats{}
	if lim != nil {
		st = lim.Stats()
	}
	o.metricsMu.Lock()
	poolQueued := o.poolQueued
	peak := o.peakQueued
	o.metricsMu.Unlock()
	sum := poolQueued + st.Queued
	if sum > peak {
		peak = sum
	}
	return ConcurrencyStats{
		Inflight:        st.Inflight,
		Queued:          sum,
		Rejected:        st.Rejected,
		PeakInflight:    st.PeakInflight,
		PeakQueued:      peak,
		QuotaPeakQueued: st.PeakQueued,
		PoolQueued:      poolQueued,
		QuotaQueued:     st.Queued,
		Admitted:        st.Admitted,
		Workers:         workers,
		GlobalLimit:     global,
		PerAgentLimit:   per,
	}
}

func (o *Orchestrator) snapshotConc() (int, *quota.Limiter) {
	o.concMu.RLock()
	defer o.concMu.RUnlock()
	return o.workers, o.limiter
}

func (o *Orchestrator) addPoolQueued(delta int) {
	o.metricsMu.Lock()
	o.poolQueued += delta
	if o.poolQueued < 0 {
		o.poolQueued = 0
	}
	local := o.poolQueued
	o.metricsMu.Unlock()
	q := 0
	o.concMu.RLock()
	lim := o.limiter
	o.concMu.RUnlock()
	if lim != nil {
		q = lim.Stats().Queued
	}
	o.notePeak(local + q)
}

func (o *Orchestrator) recordQueuePeak() {
	o.concMu.RLock()
	lim := o.limiter
	o.concMu.RUnlock()
	queued := 0
	if lim != nil {
		queued = lim.Stats().Queued
	}
	o.metricsMu.Lock()
	local := o.poolQueued
	o.metricsMu.Unlock()
	o.notePeak(local + queued)
}

func (o *Orchestrator) notePeak(total int) {
	o.metricsMu.Lock()
	if total > o.peakQueued {
		o.peakQueued = total
	}
	o.metricsMu.Unlock()
}

type execJob struct {
	idx   int
	st    SubTask
	agent *core.Agent
}

type execOutcome struct {
	output string
	err    error
}

// executeSubtasks runs every subtask on a bounded worker pool. Picks happen
// on the caller goroutine so round-robin order stays deterministic. The pool
// joins before returning so the caller can verify in task order.
func (o *Orchestrator) executeSubtasks(ctx context.Context, workers int, lim *quota.Limiter, subtasks []SubTask) []execOutcome {
	n := len(subtasks)
	out := make([]execOutcome, n)
	if n == 0 {
		return out
	}
	if workers > n {
		workers = n
	}
	if workers < 1 {
		workers = 1
	}
	if lim == nil {
		lim = quota.New(workers, workers)
	}

	jobs := make(chan execJob)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				o.addPoolQueued(-1)
				out[job.idx] = runExecutor(ctx, lim, job)
			}
		}()
	}

	o.addPoolQueued(n)
	func() {
		defer close(jobs)
		for i, st := range subtasks {
			job := execJob{idx: i, st: st, agent: o.pickRole(RoleExecutor)}
			select {
			case <-ctx.Done():
				for j := i; j < n; j++ {
					o.addPoolQueued(-1)
					lim.Reject()
					out[j].err = fmt.Errorf("executor %s: %w: %w", subtasks[j].ID, quota.ErrRejected, ctx.Err())
				}
				return
			case jobs <- job:
			}
		}
	}()
	wg.Wait()
	return out
}

func runExecutor(ctx context.Context, lim *quota.Limiter, job execJob) execOutcome {
	if job.agent == nil {
		return execOutcome{err: ErrNoExecutor}
	}
	release, err := lim.Acquire(ctx, job.agent.Name())
	if err != nil {
		return execOutcome{err: fmt.Errorf("executor %s: %w", job.st.ID, err)}
	}
	defer release()
	out, err := job.agent.Run(ctx, job.st.Input)
	if err != nil {
		return execOutcome{err: fmt.Errorf("executor %s: %w", job.st.ID, err)}
	}
	return execOutcome{output: out}
}
