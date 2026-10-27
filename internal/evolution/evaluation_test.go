package evolution

import "testing"

func TestDescribeEvaluationSeparatesProxyFromRealAnswer(t *testing.T) {
	plain := DescribeEvaluation(RetrievalRun{}, RetrievalRun{})
	if plain.SourceHit != LabelSourceHit || plain.LexicalProxy != LabelLexicalProxyNotRun || plain.RealAnswer != LabelRealAnswerUnevaluated {
		t.Fatalf("plain=%+v", plain)
	}
	scored := DescribeEvaluation(
		RetrievalRun{QualityMode: "deterministic_proxy", Quality: 0.25, PassRate: 0.5, GroundingFailures: 2},
		RetrievalRun{QualityMode: "deterministic_proxy", Quality: 0.4, PassRate: 0.75, GroundingFailures: 1},
	)
	if scored.LexicalProxy != LabelLexicalProxyEvaluated || scored.RealAnswer != LabelRealAnswerUnevaluated {
		t.Fatalf("labels=%+v", scored)
	}
	if scored.BeforeQuality != 0.25 || scored.AfterQuality != 0.4 || scored.BeforePassRate != 0.5 || scored.AfterPassRate != 0.75 || scored.BeforeGroundingFailures != 2 || scored.AfterGroundingFailures != 1 {
		t.Fatalf("scores=%+v", scored)
	}
}
