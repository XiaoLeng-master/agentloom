package eval

import "testing"

func TestDuplicateCitationsDoNotInflateScore(t *testing.T) {
	score := RAGEvaluator{}.Eval(Trace{Citations: []string{"a", "a"}, Expected: Expected{GroundedIn: []string{"a", "b"}}})
	if score.Value != 0.5 {
		t.Fatalf("score=%v", score.Value)
	}
}
func TestExtraToolReducesPrecision(t *testing.T) {
	score := SkillEvaluator{}.Eval(Trace{Tools: []string{"expected", "unsafe"}, Expected: Expected{Tools: []string{"expected"}}})
	if score.Value != 0.5 {
		t.Fatalf("score=%v", score.Value)
	}
}
func TestInvalidJudgeDistributionRejected(t *testing.T) {
	for _, probs := range []map[string]float32{{"": 1}, {"6": 1}, {"1": -1}} {
		score := GEval{Prober: func(string, string) map[string]float32 { return probs }}.Eval(Trace{})
		if score.Pass || score.Value != 0 {
			t.Fatalf("score=%+v", score)
		}
	}
}
