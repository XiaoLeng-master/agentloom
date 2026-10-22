package eval

import (
	"testing"
	"time"
)

func TestRegistry_AllEvaluators(t *testing.T) {
	r := NewRegistry(
		PromptEvaluator{},
		RAGEvaluator{},
		SkillEvaluator{},
		AgentEvaluator{MaxLatencyMs: 1000},
		GEval{},
	)
	tr := Trace{
		ID:     "t1",
		Query:  "how does ReAct work",
		Answer: "ReAct loops think act observe",
		Citations: []string{"internal/core/agent.go"},
		Tools:  []string{"echo"},
		Events: []Event{{Kind: EventLLM, Latency: 50 * time.Millisecond}},
		Expected: Expected{
			Answer:     "ReAct loops think act observe",
			Tools:      []string{"echo"},
			GroundedIn: []string{"internal/core/agent.go"},
		},
	}
	scores := r.Eval(tr)
	if len(scores) != 5 {
		t.Fatalf("want 5 scores, got %d", len(scores))
	}
	rep := Build(tr, r)
	if !rep.Pass {
		// all evaluators should pass on this well-formed trace
		t.Fatalf("expected pass, got fail: %+v", rep.Scores)
	}
	md := rep.Markdown()
	if !contains(md, "t1") || !contains(md, "geval") {
		t.Fatalf("markdown missing fields: %s", md)
	}
}

func TestRAGEvaluator_Hallucination(t *testing.T) {
	r := RAGEvaluator{}
	tr := Trace{
		Answer:    "made up answer",
		Citations: []string{"not-in-expected"},
		Expected:  Expected{GroundedIn: []string{"internal/core/agent.go"}},
	}
	s := r.Eval(tr)
	if s.Pass {
		t.Fatalf("hallucinated citations should fail rag eval")
	}
	if s.Value != 0 {
		t.Fatalf("rag recall should be 0, got %.2f", s.Value)
	}
}

func TestGEval_LogprobsWeighted(t *testing.T) {
	g := GEval{} // DefaultProber
	// high overlap -> high score
	high := g.Eval(Trace{Answer: "react think act observe", Expected: Expected{Answer: "react think act observe loop"}})
	low := g.Eval(Trace{Answer: "completely different text", Expected: Expected{Answer: "react think act observe"}})
	if high.Value <= low.Value {
		t.Fatalf("high overlap should score higher: high=%.3f low=%.3f", high.Value, low.Value)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
