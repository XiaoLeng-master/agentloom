package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/quota"
)

// holdLLM blocks inside Complete so tests can observe the concurrency cap.
// entered is the number of calls currently blocked.
type holdLLM struct {
	entered *atomic.Int32
	release <-chan struct{}
}

func (h *holdLLM) Complete(ctx context.Context, msgs []core.Message) (core.Message, error) {
	if h.entered != nil {
		h.entered.Add(1)
		defer h.entered.Add(-1)
	}
	select {
	case <-h.release:
		in := ""
		if n := len(msgs); n > 0 {
			in = msgs[n-1].Content
		}
		return core.Message{Role: core.RoleAssistant, Content: in}, nil
	case <-ctx.Done():
		return core.Message{}, ctx.Err()
	}
}

func waitAtLeast(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n.Load() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("entered=%d, want at least %d", n.Load(), want)
}

func planTasks(n int) (string, []string) {
	tasks := make([]SubTask, n)
	verdicts := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t%d", i)
		tasks[i] = SubTask{ID: id, Input: id}
		verdicts[i] = mustJSON(Verdict{TaskID: id, Pass: true})
	}
	return mustJSON(tasks), verdicts
}

func TestWorkerPoolCapsInflight(t *testing.T) {
	const workers = 2
	const tasks = 6
	release := make(chan struct{})
	var entered atomic.Int32
	plan, verdicts := planTasks(tasks)
	o := NewOrchestrator(0)
	o.SetConcurrency(ConcurrencyConfig{Workers: workers, GlobalLimit: 100, PerAgentLimit: 100})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{plan}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", &holdLLM{entered: &entered, release: release}, nil, 1))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: verdicts}))

	errCh := make(chan error, 1)
	go func() {
		_, _, err := o.Run(context.Background(), "fanout")
		errCh <- err
	}()
	waitAtLeast(t, &entered, workers)
	time.Sleep(40 * time.Millisecond)
	if got := entered.Load(); got > workers {
		t.Fatalf("executor inflight=%d exceeded worker pool %d", got, workers)
	}
	if q := o.ConcurrencyStats().QuotaQueued; q != 0 {
		t.Fatalf("quota queue=%d, global limit is above the pool so only the pool should queue", q)
	}
	if peak := o.ConcurrencyStats().PeakQueued; peak < tasks-workers {
		t.Fatalf("peak queued=%d, want at least the pool backlog %d", peak, tasks-workers)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if peak := o.ConcurrencyStats().PeakInflight; peak > workers {
		t.Fatalf("peak inflight=%d exceeded workers %d", peak, workers)
	}
}

func TestGlobalQuotaCapsInflight(t *testing.T) {
	const global = 2
	const tasks = 6
	release := make(chan struct{})
	var entered atomic.Int32
	plan, verdicts := planTasks(tasks)
	o := NewOrchestrator(0)
	o.SetConcurrency(ConcurrencyConfig{Workers: tasks, GlobalLimit: global, PerAgentLimit: tasks})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{plan}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", &holdLLM{entered: &entered, release: release}, nil, 1))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: verdicts}))

	errCh := make(chan error, 1)
	go func() {
		_, _, err := o.Run(context.Background(), "fanout")
		errCh <- err
	}()
	waitAtLeast(t, &entered, global)
	time.Sleep(40 * time.Millisecond)
	if got := entered.Load(); got > global {
		t.Fatalf("executor inflight=%d exceeded global quota %d", got, global)
	}
	if got := o.ConcurrencyStats().QuotaQueued; got < 1 {
		t.Fatalf("quota queued=%d, excess tasks should wait on the global semaphore", got)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if peak := o.ConcurrencyStats().PeakInflight; peak > global {
		t.Fatalf("peak inflight=%d exceeded global %d", peak, global)
	}
}

func TestPerAgentQuota(t *testing.T) {
	const tasks = 6
	release := make(chan struct{})
	var enteredA, enteredB atomic.Int32
	plan, verdicts := planTasks(tasks)
	o := NewOrchestrator(0)
	o.SetConcurrency(ConcurrencyConfig{Workers: tasks, GlobalLimit: 10, PerAgentLimit: 1})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{plan}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec-a", "x", &holdLLM{entered: &enteredA, release: release}, nil, 1))
	o.AddAgent(RoleExecutor, core.NewAgent("exec-b", "x", &holdLLM{entered: &enteredB, release: release}, nil, 1))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: verdicts}))

	errCh := make(chan error, 1)
	go func() {
		_, _, err := o.Run(context.Background(), "fanout")
		errCh <- err
	}()
	waitAtLeast(t, &enteredA, 1)
	waitAtLeast(t, &enteredB, 1)
	time.Sleep(40 * time.Millisecond)
	if got := enteredA.Load(); got > 1 {
		t.Fatalf("exec-a inflight=%d, per-agent limit is 1", got)
	}
	if got := enteredB.Load(); got > 1 {
		t.Fatalf("exec-b inflight=%d, per-agent limit is 1", got)
	}
	// The two agents may run together; the global limit is what would stop that.
	if got := enteredA.Load() + enteredB.Load(); got > 2 {
		t.Fatalf("combined inflight=%d, want at most one per agent", got)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestQuotaTimeoutRejects(t *testing.T) {
	release := make(chan struct{})
	var entered atomic.Int32
	o := NewOrchestrator(0)
	o.SetConcurrency(ConcurrencyConfig{Workers: 2, GlobalLimit: 1, PerAgentLimit: 1})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "hold"}}),
		mustJSON([]SubTask{{ID: "t1", Input: "wait"}}),
	}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", &holdLLM{entered: &entered, release: release}, nil, 1))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{
		mustJSON(Verdict{TaskID: "t1", Pass: true}),
	}}))

	first := make(chan error, 1)
	go func() {
		_, _, err := o.Run(context.Background(), "first")
		first <- err
	}()
	waitAtLeast(t, &entered, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, _, err := o.Run(ctx, "second")
	if !errors.Is(err, quota.ErrRejected) {
		t.Fatalf("timed-out waiter should be rejected, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rejection should wrap the deadline, got %v", err)
	}
	if got := o.ConcurrencyStats().Rejected; got < 1 {
		t.Fatalf("rejected=%d, want at least 1", got)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestResultOrderStable(t *testing.T) {
	o := NewOrchestrator(0)
	o.SetConcurrency(ConcurrencyConfig{Workers: 3, GlobalLimit: 3, PerAgentLimit: 3})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{
			{ID: "t1", Input: "slow-a"},
			{ID: "t2", Input: "mid-b"},
			{ID: "t3", Input: "fast-c"},
		}),
	}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", delayLLM{}, nil, 1))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{
		mustJSON(Verdict{TaskID: "t1", Pass: true}),
		mustJSON(Verdict{TaskID: "t2", Pass: true}),
		mustJSON(Verdict{TaskID: "t3", Pass: true}),
	}}))

	out, trace, err := o.Run(context.Background(), "order")
	if err != nil {
		t.Fatal(err)
	}
	const want = "slow-a\nmid-b\nfast-c"
	if out != want {
		t.Fatalf("summary=%q, want %q (task order, not completion order)", out, want)
	}
	var execIDs []string
	for _, c := range trace {
		if c.Phase == PhaseExecute {
			execIDs = append(execIDs, c.TaskID)
		}
	}
	if strings.Join(execIDs, ",") != "t1,t2,t3" {
		t.Fatalf("execute trace order=%v, want t1,t2,t3", execIDs)
	}
}

// delayLLM sleeps longer for earlier tasks so completion order is the reverse
// of task order. It echoes the user input.
type delayLLM struct{}

func (delayLLM) Complete(ctx context.Context, msgs []core.Message) (core.Message, error) {
	in := ""
	if n := len(msgs); n > 0 {
		in = msgs[n-1].Content
	}
	var d time.Duration
	switch {
	case strings.HasPrefix(in, "slow"):
		d = 60 * time.Millisecond
	case strings.HasPrefix(in, "mid"):
		d = 20 * time.Millisecond
	}
	if d > 0 {
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return core.Message{}, ctx.Err()
		case <-timer.C:
		}
	}
	return core.Message{Role: core.RoleAssistant, Content: in}, nil
}
