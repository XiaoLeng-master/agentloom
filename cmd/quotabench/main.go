// Command quotabench is a local load probe for the executor worker pool and
// the two-level concurrency quota. It does not call a model API. Every
// planner, executor, and verifier is an in-process stub; the executor sleeps
// so overlapping calls are visible.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/orchestrator"
)

func main() {
	workers := flag.Int("workers", 4, "executor worker-pool size per turn")
	global := flag.Int("global", 3, "global in-flight executor limit")
	perAgent := flag.Int("per-agent", 2, "per-executor-agent in-flight limit")
	requests := flag.Int("requests", 12, "concurrent Run calls")
	tasks := flag.Int("tasks", 4, "subtasks per Run")
	execSleep := flag.Duration("exec-sleep", 20*time.Millisecond, "simulated executor latency")
	shortN := flag.Int("short-timeout-requests", 4, "how many Runs use the short timeout")
	shortTO := flag.Duration("short-timeout", 30*time.Millisecond, "timeout for the short Runs")
	longTO := flag.Duration("long-timeout", 5*time.Second, "timeout for the remaining Runs")
	flag.Parse()

	if *shortN > *requests {
		fmt.Fprintln(os.Stderr, "short-timeout-requests cannot exceed requests")
		os.Exit(2)
	}

	counter := &execCounter{sleep: *execSleep, per: map[string]int{}, perPeak: map[string]int{}}
	plan := planLLM{body: subtasksJSON(*tasks)}
	o := orchestrator.NewOrchestrator(0)
	if err := o.SetConcurrency(orchestrator.ConcurrencyConfig{
		Workers:       *workers,
		GlobalLimit:   *global,
		PerAgentLimit: *perAgent,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	o.AddAgent(orchestrator.RolePlanner, core.NewAgent("planner", "plan", plan, nil, 1))
	o.AddAgent(orchestrator.RoleExecutor, core.NewAgent("exec-a", "exec", &execLLM{name: "exec-a", c: counter}, nil, 1))
	o.AddAgent(orchestrator.RoleExecutor, core.NewAgent("exec-b", "exec", &execLLM{name: "exec-b", c: counter}, nil, 1))
	o.AddAgent(orchestrator.RoleVerifier, core.NewAgent("verifier", "verify", verLLM{}, nil, 1))

	started := time.Now()
	var wg sync.WaitGroup
	var okN, failN int
	var mu sync.Mutex
	for i := 0; i < *requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			limit := *longTO
			if i < *shortN {
				limit = *shortTO
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			_, _, err := o.Run(ctx, "load")
			mu.Lock()
			if err != nil {
				failN++
			} else {
				okN++
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	finished := time.Now()
	stats := o.ConcurrencyStats()
	counter.mu.Lock()
	perPeak := map[string]int{}
	for k, v := range counter.perPeak {
		perPeak[k] = v
	}
	observed := counter.peak
	counter.mu.Unlock()

	report := map[string]any{
		"workers":                 *workers,
		"global_limit":            *global,
		"per_agent_limit":         *perAgent,
		"requests":                *requests,
		"subtasks_per_request":    *tasks,
		"exec_sleep_ms":           execSleep.Milliseconds(),
		"short_timeout_requests":  *shortN,
		"short_timeout_ms":        shortTO.Milliseconds(),
		"long_timeout_ms":         longTO.Milliseconds(),
		"runs_ok":                 okN,
		"runs_failed":             failN,
		"observed_peak_inflight":  observed,
		"observed_per_agent_peak": perPeak,
		"quota_peak_inflight":     stats.PeakInflight,
		"quota_peak_queued":       stats.QuotaPeakQueued,
		"combined_peak_queued":    stats.PeakQueued,
		"rejected":                stats.Rejected,
		"admitted":                stats.Admitted,
		"inflight_end":            stats.Inflight,
		"queued_end":              stats.Queued,
		"wall_ms":                 finished.Sub(started).Milliseconds(),
		"started_at":              started.UTC().Format(time.RFC3339Nano),
		"finished_at":             finished.UTC().Format(time.RFC3339Nano),
		"go_version":              runtime.Version(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if observed > *global {
		fmt.Fprintf(os.Stderr, "observed peak inflight %d exceeded global limit %d\n", observed, *global)
		os.Exit(1)
	}
	for name, peak := range perPeak {
		if peak > *perAgent {
			fmt.Fprintf(os.Stderr, "agent %s peak %d exceeded per-agent limit %d\n", name, peak, *perAgent)
			os.Exit(1)
		}
	}
	if stats.PeakInflight > *global {
		fmt.Fprintf(os.Stderr, "quota peak inflight %d exceeded global limit %d\n", stats.PeakInflight, *global)
		os.Exit(1)
	}
	if stats.Inflight != 0 || stats.Queued != 0 {
		fmt.Fprintf(os.Stderr, "gauge leak inflight=%d queued=%d\n", stats.Inflight, stats.Queued)
		os.Exit(1)
	}
}

func subtasksJSON(n int) string {
	tasks := make([]orchestrator.SubTask, n)
	for i := 0; i < n; i++ {
		tasks[i] = orchestrator.SubTask{ID: fmt.Sprintf("t%d", i), Input: fmt.Sprintf("task-%d", i)}
	}
	b, err := json.Marshal(tasks)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type planLLM struct{ body string }

func (p planLLM) Complete(context.Context, []core.Message) (core.Message, error) {
	return core.Message{Role: core.RoleAssistant, Content: p.body}, nil
}

type verLLM struct{}

func (verLLM) Complete(_ context.Context, msgs []core.Message) (core.Message, error) {
	in := ""
	if n := len(msgs); n > 0 {
		in = msgs[n-1].Content
	}
	var er struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal([]byte(in), &er)
	out, _ := json.Marshal(map[string]any{"task_id": er.TaskID, "pass": true})
	return core.Message{Role: core.RoleAssistant, Content: string(out)}, nil
}

type execCounter struct {
	mu       sync.Mutex
	sleep    time.Duration
	inflight int
	peak     int
	per      map[string]int
	perPeak  map[string]int
}

func (c *execCounter) enter(name string) func() {
	c.mu.Lock()
	c.inflight++
	if c.inflight > c.peak {
		c.peak = c.inflight
	}
	c.per[name]++
	if c.per[name] > c.perPeak[name] {
		c.perPeak[name] = c.per[name]
	}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.inflight--
		c.per[name]--
		c.mu.Unlock()
	}
}

type execLLM struct {
	name string
	c    *execCounter
}

func (e *execLLM) Complete(ctx context.Context, _ []core.Message) (core.Message, error) {
	leave := e.c.enter(e.name)
	defer leave()
	timer := time.NewTimer(e.c.sleep)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return core.Message{}, ctx.Err()
	case <-timer.C:
		return core.Message{Role: core.RoleAssistant, Content: "ok"}, nil
	}
}
