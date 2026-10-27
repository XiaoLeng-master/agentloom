package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/core"
)

type panicLLM struct{}

func (panicLLM) Complete(context.Context, []core.Message) (core.Message, error) {
	panic("boom")
}

func TestExecutorPanicBecomesError(t *testing.T) {
	before := runtime.NumGoroutine()
	o := NewOrchestrator(0)
	mustSet(t, o, ConcurrencyConfig{Workers: 2, GlobalLimit: 2, PerAgentLimit: 2})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t0", Input: "explode"}}),
	}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", panicLLM{}, nil, 1))

	_, _, err := o.Run(context.Background(), "panic")
	if err == nil || !strings.Contains(err.Error(), "panic") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("panic should become an executor error, got %v", err)
	}
	st := o.ConcurrencyStats()
	if st.Inflight != 0 || st.Queued != 0 {
		t.Fatalf("panic leaked a slot: %+v", st)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before {
		runtime.Gosched()
		time.Sleep(5 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("goroutines before=%d after=%d", before, got)
	}
}

type cancelLLM struct {
	started   atomic.Int32
	cancelled atomic.Int32
	finished  atomic.Int32
}

func (c *cancelLLM) Complete(ctx context.Context, msgs []core.Message) (core.Message, error) {
	in := ""
	if n := len(msgs); n > 0 {
		in = msgs[n-1].Content
	}
	c.started.Add(1)
	if in == "t0" {
		deadline := time.Now().Add(time.Second)
		for c.started.Load() < 4 && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return core.Message{}, ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
		return core.Message{}, errors.New("fail-fast")
	}
	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		c.cancelled.Add(1)
		return core.Message{}, ctx.Err()
	case <-timer.C:
		c.finished.Add(1)
		return core.Message{Role: core.RoleAssistant, Content: "late"}, nil
	}
}

func TestFirstErrorCancelsSiblings(t *testing.T) {
	llm := &cancelLLM{}
	o := NewOrchestrator(0)
	mustSet(t, o, ConcurrencyConfig{Workers: 4, GlobalLimit: 8, PerAgentLimit: 8})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{
			{ID: "t0", Input: "t0"},
			{ID: "t1", Input: "t1"},
			{ID: "t2", Input: "t2"},
			{ID: "t3", Input: "t3"},
		}),
	}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", llm, nil, 1))

	start := time.Now()
	_, trace, err := o.Run(context.Background(), "cancel")
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "fail-fast") {
		t.Fatalf("err=%v", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("Run waited %s for siblings after the first error", elapsed)
	}
	if llm.finished.Load() != 0 {
		t.Fatalf("siblings ran to completion: finished=%d", llm.finished.Load())
	}
	if llm.started.Load() != 4 || llm.cancelled.Load() != 3 {
		t.Fatalf("started=%d cancelled=%d, want 4 started and 3 cancelled", llm.started.Load(), llm.cancelled.Load())
	}
	execs := 0
	sawFail := false
	for _, c := range trace {
		if c.Phase != PhaseExecute {
			continue
		}
		execs++
		if strings.Contains(c.Payload, "fail-fast") {
			sawFail = true
		}
	}
	if execs != 4 || !sawFail {
		t.Fatalf("execute contracts=%d sawFail=%v, want every subtask including the failure", execs, sawFail)
	}
	if st := o.ConcurrencyStats(); st.Inflight != 0 || st.Queued != 0 {
		t.Fatalf("cancel leaked a slot: %+v", st)
	}
}

func TestCombinedPeakDoesNotExceedAttempts(t *testing.T) {
	const tasks = 20
	const runs = 6
	attempts := tasks * runs
	o := NewOrchestrator(0)
	mustSet(t, o, ConcurrencyConfig{Workers: 4, GlobalLimit: 2, PerAgentLimit: 2})
	o.AddAgent(RolePlanner, core.NewAgent("planner", "plan", fixedPlan{body: planBody(tasks)}, nil, 1))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", napLLM{}, nil, 1))
	o.AddAgent(RoleVerifier, core.NewAgent("verifier", "verify", passVerifier{}, nil, 1))

	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := o.Run(context.Background(), "peak"); err != nil {
				t.Errorf("run: %v", err)
			}
		}()
	}
	wg.Wait()
	st := o.ConcurrencyStats()
	if st.PeakQueued > attempts {
		t.Fatalf("combined peak %d exceeded %d attempts", st.PeakQueued, attempts)
	}
	if st.Inflight != 0 || st.Queued != 0 {
		t.Fatalf("leak %+v", st)
	}
}

func TestSetConcurrencyRejectedWhileRunning(t *testing.T) {
	release := make(chan struct{})
	var entered atomic.Int32
	o := NewOrchestrator(0)
	mustSet(t, o, ConcurrencyConfig{Workers: 1, GlobalLimit: 1, PerAgentLimit: 1})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t0", Input: "hold"}}),
	}}))
	o.AddAgent(RoleExecutor, core.NewAgent("exec", "x", &holdLLM{entered: &entered, release: release}, nil, 1))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{
		mustJSON(Verdict{TaskID: "t0", Pass: true}),
	}}))

	errCh := make(chan error, 1)
	go func() {
		_, _, err := o.Run(context.Background(), "busy")
		errCh <- err
	}()
	waitAtLeast(t, &entered, 1)
	if err := o.SetConcurrency(ConcurrencyConfig{Workers: 1, GlobalLimit: 1, PerAgentLimit: 1}); !errors.Is(err, ErrConcurrencyBusy) {
		t.Fatalf("SetConcurrency during Run: %v", err)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if err := o.SetConcurrency(ConcurrencyConfig{Workers: 2, GlobalLimit: 2, PerAgentLimit: 2}); err != nil {
		t.Fatal(err)
	}
}

type fixedPlan struct{ body string }

func (p fixedPlan) Complete(context.Context, []core.Message) (core.Message, error) {
	return core.Message{Role: core.RoleAssistant, Content: p.body}, nil
}

func planBody(n int) string {
	tasks := make([]SubTask, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("t%d", i)
		tasks[i] = SubTask{ID: id, Input: id}
	}
	b, _ := json.Marshal(tasks)
	return string(b)
}

type napLLM struct{}

func (napLLM) Complete(ctx context.Context, _ []core.Message) (core.Message, error) {
	timer := time.NewTimer(2 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return core.Message{}, ctx.Err()
	case <-timer.C:
		return core.Message{Role: core.RoleAssistant, Content: "ok"}, nil
	}
}

type passVerifier struct{}

func (passVerifier) Complete(_ context.Context, msgs []core.Message) (core.Message, error) {
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
