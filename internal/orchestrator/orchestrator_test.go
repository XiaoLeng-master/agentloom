package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/core"
)

// scriptLLM implements core.LLMClient by replaying a fixed script. It lets
// the orchestrator tests drive planner/executor/verifier agents without a
// real model — each role gets its own scriptLLM with a deterministic script.
type scriptLLM struct {
	script []string
	i      int32
}

func (s *scriptLLM) Complete(_ context.Context, _ []core.Message) (core.Message, error) {
	i := atomic.AddInt32(&s.i, 1) - 1
	if int(i) >= len(s.script) {
		return core.Message{}, errors.New("script exhausted")
	}
	return core.Message{Role: core.RoleAssistant, Content: s.script[i]}, nil
}

// newAgent builds a core.Agent wired to a scriptLLM so the orchestrator tests
// stay deterministic and offline.
func newAgent(llm *scriptLLM) *core.Agent {
	return core.NewAgent("test", "you are a test agent", llm, nil, 1)
}

// mustJSON panics on marshal failure — keeps test setup terse.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestHappyPath(t *testing.T) {
	o := NewOrchestrator(2)
	// Planner emits two subtasks as JSON.
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "search docs"}, {ID: "t2", Input: "summarize"}}),
	}}))
	// Executor answers both in order.
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{
		"found: ReAct loop", "summary: plan-act-observe",
	}}))
	// Verifier passes both.
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{
		mustJSON(Verdict{TaskID: "t1", Pass: true}),
		mustJSON(Verdict{TaskID: "t2", Pass: true}),
	}}))

	out, trace, err := o.Run(context.Background(), "build a ReAct explainer")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "found: ReAct loop") || !strings.Contains(out, "summary") {
		t.Fatalf("result missing executor outputs: %q", out)
	}
	// Trace must carry plan + 2 execute + 2 verify = 5 contracts.
	if len(trace) != 5 {
		t.Fatalf("trace len = %d, want 5", len(trace))
	}
	if trace[0].Phase != PhasePlan || trace[0].From != RolePlanner {
		t.Fatalf("trace[0] = %+v, want plan from planner", trace[0])
	}
	execs, vers := 0, 0
	for _, c := range trace {
		switch c.Phase {
		case PhaseExecute:
			execs++
		case PhaseVerify:
			vers++
		}
	}
	if execs != 2 || vers != 2 {
		t.Fatalf("execs=%d vers=%d, want 2/2", execs, vers)
	}
}

func TestReplanOnVerifyFail(t *testing.T) {
	o := NewOrchestrator(2)
	// Planner: first call splits, second call (re-plan) keeps one task.
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "do it"}}),
		mustJSON([]SubTask{{ID: "t1", Input: "do it again"}}),
	}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{
		"first try", "second try",
	}}))
	// Verifier fails the first pass, passes the second.
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{
		mustJSON(Verdict{TaskID: "t1", Pass: false, Reason: "incomplete"}),
		mustJSON(Verdict{TaskID: "t1", Pass: true}),
	}}))

	out, trace, err := o.Run(context.Background(), "do something")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "second try") {
		t.Fatalf("result should be the second executor attempt: %q", out)
	}
	plans := 0
	for _, c := range trace {
		if c.Phase == PhasePlan {
			plans++
		}
	}
	if plans != 2 {
		t.Fatalf("plan count = %d, want 2 (initial + one re-plan)", plans)
	}
}

func TestExecutorRoundRobin(t *testing.T) {
	o := NewOrchestrator(0)
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "x"}, {ID: "t2", Input: "y"}}),
	}}))
	// Two executors with distinct outputs — round-robin must hit both.
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"from-A"}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"from-B"}}))

	out, _, err := o.Run(context.Background(), "split")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "from-A") || !strings.Contains(out, "from-B") {
		t.Fatalf("round-robin should hit both executors, got %q", out)
	}
}

func TestNoPlannerFails(t *testing.T) {
	o := NewOrchestrator(0)
	_, _, err := o.Run(context.Background(), "anything")
	if !errors.Is(err, ErrNoPlanner) {
		t.Fatalf("want ErrNoPlanner, got %v", err)
	}
}

func TestNoExecutorFails(t *testing.T) {
	o := NewOrchestrator(0)
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "x"}}),
	}}))
	_, _, err := o.Run(context.Background(), "anything")
	if !errors.Is(err, ErrNoExecutor) {
		t.Fatalf("want ErrNoExecutor, got %v", err)
	}
}

// TestNoVerifierNonFatal: a missing verifier pool is not an error — the
// orchestrator just records the execute leg and finishes. This matches the
// "rules + small model" verifier being optional in some deployments.
func TestNoVerifierNonFatal(t *testing.T) {
	o := NewOrchestrator(0)
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "x"}}),
	}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"done"}}))
	out, trace, err := o.Run(context.Background(), "x")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("result = %q, want done", out)
	}
	// trace = 1 plan + 1 execute, no verify.
	if len(trace) != 2 {
		t.Fatalf("trace len = %d, want 2", len(trace))
	}
	for _, c := range trace {
		if c.Phase == PhaseVerify {
			t.Fatal("trace must not contain verify when no verifier mounted")
		}
	}
}

// TestPlannerFallbackTask: when the planner does not emit JSON subtasks, its
// raw output becomes a single fallback task — degraded, not crashed.
func TestPlannerFallbackTask(t *testing.T) {
	o := NewOrchestrator(0)
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{"just answer directly"}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"echoed"}}))
	out, _, err := o.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "echoed") {
		t.Fatalf("result = %q, want echoed", out)
	}
}

// TestConcurrentRuns: multiple orchestrator turns in parallel must not race.
// Each Run picks its own planner/executor/verifier via round-robin; the
// refcount-free pool uses atomic counters, so the race detector should pass.
func TestConcurrentRuns(t *testing.T) {
	o := NewOrchestrator(0)
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{
		mustJSON([]SubTask{{ID: "t1", Input: "x"}}),
		mustJSON([]SubTask{{ID: "t1", Input: "x"}}),
		mustJSON([]SubTask{{ID: "t1", Input: "x"}}),
		mustJSON([]SubTask{{ID: "t1", Input: "x"}}),
	}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"a", "b", "c", "d"}}))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := o.Run(context.Background(), "x"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
