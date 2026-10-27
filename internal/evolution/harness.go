package evolution

import (
	"github.com/XiaoLeng-master/agentloom/internal/eval"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

// HarnessRun is the reproducible quality side of an evolution run. The
// deterministic_proxy mode evaluates retrieved context and citations; it is
// intentionally not presented as a live LLM judge or claim-level faithfulness.
type HarnessRun struct {
	Mode              string             `json:"mode"`
	Cases             int                `json:"cases"`
	Quality           float64            `json:"quality"`
	GroundingFailures int                `json:"grounding_failures"`
	PassRate          float64            `json:"pass_rate"`
	Scores            map[string]float64 `json:"scores"`
}

func RunHarness(cases []Case, docs []rag.Document, splitter rag.Splitter) HarnessRun {
	r := &rag.Retriever{Splitter: splitter, Embedder: rag.HashEmbedder{DimV: 512}, Store: &rag.MemoryStore{}}
	r.Index(docs)
	registry := eval.NewRegistry(eval.PromptEvaluator{}, eval.RAGEvaluator{}, eval.SkillEvaluator{}, eval.AgentEvaluator{}, eval.GEval{})
	out := HarnessRun{Mode: "deterministic_proxy", Cases: len(cases), Scores: map[string]float64{}}
	passed := 0
	for _, c := range cases {
		hits := r.Retrieve(c.Question, 3)
		var citations []string
		var answer string
		for _, hit := range hits {
			citations = append(citations, hit.Source)
			if answer == "" {
				answer = hit.Text
			}
		}
		trace := eval.Trace{ID: c.ID, Query: c.Question, Answer: answer, Citations: citations, Tools: []string{"search_docs"}, Expected: eval.Expected{Answer: c.GroundTruth, Tools: []string{"search_docs"}, GroundedIn: []string{c.SourceDoc}}}
		report := eval.Build(trace, registry)
		if report.Pass {
			passed++
		}
		for _, score := range report.Scores {
			out.Scores[score.Name] += float64(score.Value)
			if score.Name == "rag" && !score.Pass {
				out.GroundingFailures++
			}
			if score.Name == "geval" {
				out.Quality += float64(score.Value)
			}
		}
	}
	if len(cases) > 0 {
		out.PassRate = float64(passed) / float64(len(cases))
		out.Quality /= float64(len(cases))
		for k, v := range out.Scores {
			out.Scores[k] = v / float64(len(cases))
		}
	}
	return out
}
