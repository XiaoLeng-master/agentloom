package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestConcurrentPoolGrowth(t *testing.T) {
	o := NewOrchestrator(1)
	first := newAgent(&scriptLLM{script: []string{"ok"}})
	o.AddAgent(RoleExecutor, first)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); o.AddAgent(RoleExecutor, first) }()
		go func() {
			defer wg.Done()
			if o.pickRole(RoleExecutor) == nil {
				t.Error("empty pool")
			}
		}()
	}
	wg.Wait()
	if got := o.PoolSize(RoleExecutor); got != 101 {
		t.Fatalf("size=%d", got)
	}
}

func TestVerifierFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script []string
	}{
		{"error", nil}, {"invalid-json", []string{"broken"}},
		{"missing-pass", []string{`{"task_id":"t1"}`}},
		{"wrong-task", []string{`{"task_id":"other","pass":true}`}},
		{"null", []string{`null`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := NewOrchestrator(1)
			o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{`[{"id":"t1","input":"x"}]`}}))
			o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"unverified"}}))
			o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: tc.script}))
			out, trace, err := o.Run(context.Background(), "x")
			if !errors.Is(err, ErrVerification) || out != "" {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if len(trace) != 3 || trace[2].Phase != PhaseVerify {
				t.Fatalf("missing failure trace: %+v", trace)
			}
		})
	}
}

func TestReplanExhaustionFailsClosed(t *testing.T) {
	o := NewOrchestrator(1)
	plan := mustJSON([]SubTask{{ID: "t1", Input: "x"}})
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{plan, plan}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"bad", "still bad"}}))
	verdict := mustJSON(Verdict{TaskID: "t1", Pass: false, Reason: "unsupported"})
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{verdict, verdict}}))
	out, _, err := o.Run(context.Background(), "x")
	if out != "" || !errors.Is(err, ErrReplanExhausted) {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestReplannerErrorFailsClosed(t *testing.T) {
	o := NewOrchestrator(1)
	o.AddAgent(RolePlanner, newAgent(&scriptLLM{script: []string{mustJSON([]SubTask{{ID: "t1", Input: "x"}})}}))
	o.AddAgent(RoleExecutor, newAgent(&scriptLLM{script: []string{"bad"}}))
	o.AddAgent(RoleVerifier, newAgent(&scriptLLM{script: []string{mustJSON(Verdict{TaskID: "t1", Pass: false})}}))
	out, _, err := o.Run(context.Background(), "x")
	if out != "" || err == nil {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
