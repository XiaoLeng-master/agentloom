package eval

import "strings"

// GEval implements the logprobs-weighted scoring scheme: instead of sampling a
// single discrete score (high variance), it reads the judge model's
// probability mass over candidate score tokens ("1".."5") and computes a
// normalized weighted score. This cuts single-shot jitter.
//
// The real implementation calls a model and reads logprobs. To keep the
// harness testable without a live model, GEval takes a Prober that returns
// token->prob; DefaultProber provides a deterministic lexical baseline.
type GEval struct {
	Prober func(answer, expected string) map[string]float32
}

func (g GEval) Name() string { return "geval" }
func (g GEval) Eval(t Trace) Score {
	prober := g.Prober
	if prober == nil {
		prober = DefaultProber
	}
	probs := prober(t.Answer, t.Expected.Answer)
	var num, den float32
	for tok, p := range probs {
		w := float32(tok[0] - '0') // "1".."5" -> 1..5
		num += w * p
		den += p
	}
	if den == 0 {
		return Score{Name: "geval", Value: 0, Reason: "no judge probs"}
	}
	v := num / den / 5.0 // normalize 1..5 to 0..1
	return Score{
		Name:   "geval",
		Value:  v,
		Reason: "logprobs-weighted judge (normalized 1..5)",
		Pass:   v >= 0.6,
	}
}

// DefaultProber is a deterministic stand-in for a judge model: it buckets the
// lexical overlap between answer and expected into a probability distribution
// over score tokens "1".."5". Higher overlap shifts mass toward "5".
func DefaultProber(answer, expected string) map[string]float32 {
	overlap := jaccard(norm(answer), norm(expected))
	// map overlap [0,1] to a peak at token 1..5
	peak := int(overlap*4) + 1 // 1..5
	probs := map[string]float32{}
	for i := 1; i <= 5; i++ {
		tok := string(rune('0' + i))
		d := float32(abs(i - peak))
		probs[tok] = 1.0 / (1.0 + d) // triangle around peak
	}
	return probs
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// compile-time: GEval is an Evaluator.
var _ Evaluator = GEval{}

// Helpers reused from evaluators.go (norm, jaccard) via the same package.
// (string import kept for norm's TrimSpace usage above.)
var _ = strings.TrimSpace
