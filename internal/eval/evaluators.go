package eval

import "strings"

// PromptEvaluator scores instruction following: does the answer address the
// query and overlap with the expected answer? Lexical overlap is a cheap
// deterministic proxy; the LLM judge (GEval) refines it.
type PromptEvaluator struct{}

func (PromptEvaluator) Name() string { return "prompt" }
func (PromptEvaluator) Eval(t Trace) Score {
	v := jaccard(norm(t.Answer), norm(t.Expected.Answer))
	return Score{
		Name:   "prompt",
		Value:  v,
		Reason: "lexical overlap of answer with expected",
		Pass:   v > 0.2,
	}
}

// RAGEvaluator scores retrieval grounding: do the answer's citations cover
// the expected source documents? (Faithfulness proxy: a hallucinated answer
// cites sources that weren't retrieved.)
type RAGEvaluator struct{}

func (RAGEvaluator) Name() string { return "rag" }
func (RAGEvaluator) Eval(t Trace) Score {
	if len(t.Expected.GroundedIn) == 0 {
		return Score{Name: "rag", Value: 1, Reason: "no grounding expected", Pass: true}
	}
	hits := 0
	for _, c := range t.Citations {
		for _, g := range t.Expected.GroundedIn {
			if c == g {
				hits++
				break
			}
		}
	}
	recall := float32(hits) / float32(len(t.Expected.GroundedIn))
	return Score{
		Name:   "rag",
		Value:  recall,
		Reason: "citation recall vs expected sources (faithfulness)",
		Pass:   recall >= 0.5,
	}
}

// SkillEvaluator scores tool selection: did the agent pick the expected tools?
type SkillEvaluator struct{}

func (SkillEvaluator) Name() string { return "skill" }
func (SkillEvaluator) Eval(t Trace) Score {
	if len(t.Expected.Tools) == 0 {
		return Score{Name: "skill", Value: 1, Reason: "no tools expected", Pass: true}
	}
	want := toSet(t.Expected.Tools)
	got := toSet(t.Tools)
	inter := 0
	for w := range want {
		if got[w] {
			inter++
		}
	}
	p := float32(inter) / float32(len(want))
	return Score{
		Name:   "skill",
		Value:  p,
		Reason: "expected tool precision (did agent pick the right tools)",
		Pass:   p >= 0.5,
	}
}

// AgentEvaluator scores task completion: non-empty answer + latency in budget.
type AgentEvaluator struct {
	MaxLatencyMs int
}

func (a AgentEvaluator) Name() string { return "agent" }
func (a AgentEvaluator) Eval(t Trace) Score {
	v := float32(0)
	if strings.TrimSpace(t.Answer) != "" {
		v = 0.5
	}
	lat := t.Latency().Milliseconds()
	if a.MaxLatencyMs == 0 || lat <= int64(a.MaxLatencyMs) {
		v += 0.5
	}
	return Score{
		Name:   "agent",
		Value:  v,
		Reason: "completion + latency budget",
		Pass:   v >= 0.8,
	}
}

// helpers
func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func jaccard(a, b string) float32 {
	if a == "" || b == "" {
		return 0
	}
	sa := toSet(strings.Fields(a))
	sb := toSet(strings.Fields(b))
	if len(sa) == 0 || len(sb) == 0 {
		return 0
	}
	inter := 0
	for w := range sa {
		if sb[w] {
			inter++
		}
	}
	union := len(sa) + len(sb) - inter
	return float32(inter) / float32(union)
}

func toSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		if x != "" {
			m[x] = true
		}
	}
	return m
}
