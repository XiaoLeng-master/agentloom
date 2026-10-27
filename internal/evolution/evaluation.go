package evolution

const (
	// LabelSourceHit is the public name for Top-K source retrieval.
	LabelSourceHit = "来源命中"
	// LabelLexicalProxyEvaluated means proxy scores were computed from
	// retrieved context and lexical overlap, not from a live model answer.
	LabelLexicalProxyEvaluated = "确定性词法代理已评"
	// LabelLexicalProxyNotRun means this record has no proxy scores.
	LabelLexicalProxyNotRun = "确定性词法代理未运行"
	// LabelRealAnswerUnevaluated means no live model answer was judged.
	LabelRealAnswerUnevaluated = "真实回答未评"
)

// Evaluation names the three measurements returned by bench and evolve.
// Proxy numbers describe retrieved context; they are not an LLM Judge score.
type Evaluation struct {
	SourceHit               string  `json:"source_hit"`
	LexicalProxy            string  `json:"lexical_proxy"`
	RealAnswer              string  `json:"real_answer"`
	BeforeQuality           float64 `json:"before_quality"`
	AfterQuality            float64 `json:"after_quality"`
	BeforePassRate          float64 `json:"before_pass_rate"`
	AfterPassRate           float64 `json:"after_pass_rate"`
	BeforeGroundingFailures int     `json:"before_grounding_failures"`
	AfterGroundingFailures  int     `json:"after_grounding_failures"`
}

// DescribeEvaluation copies proxy scores and attaches the public names.
// An empty quality mode is reported as not run, never as a finished answer eval.
func DescribeEvaluation(before, after RetrievalRun) Evaluation {
	lexical := LabelLexicalProxyNotRun
	if before.QualityMode != "" || after.QualityMode != "" {
		lexical = LabelLexicalProxyEvaluated
	}
	return Evaluation{
		SourceHit:               LabelSourceHit,
		LexicalProxy:            lexical,
		RealAnswer:              LabelRealAnswerUnevaluated,
		BeforeQuality:           before.Quality,
		AfterQuality:            after.Quality,
		BeforePassRate:          before.PassRate,
		AfterPassRate:           after.PassRate,
		BeforeGroundingFailures: before.GroundingFailures,
		AfterGroundingFailures:  after.GroundingFailures,
	}
}
