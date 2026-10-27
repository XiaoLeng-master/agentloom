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

// ProxyScore is one case of deterministic_proxy: lexical overlap between
// groundTruth and the first retrieved passage, plus citation coverage.
// It is not a generated-answer score and not an LLM judge.
type ProxyScore struct {
	Quality          float64
	GroundingFailure bool
	Pass             bool
	Scores           map[string]float64
}

func newProxyRegistry() *eval.Registry {
	return eval.NewRegistry(eval.PromptEvaluator{}, eval.RAGEvaluator{}, eval.SkillEvaluator{}, eval.AgentEvaluator{}, eval.GEval{})
}

// ScoreProxy evaluates hits with the same definition RunHarness uses.
// The proxy answer is the first retrieved passage, never a rendered reply.
func ScoreProxy(c Case, hits []rag.Context, registry *eval.Registry) ProxyScore {
	if registry == nil {
		registry = newProxyRegistry()
	}
	var citations []string
	var answer string
	for _, hit := range hits {
		citations = append(citations, hit.Source)
		if answer == "" {
			answer = hit.Text
		}
	}
	trace := eval.Trace{
		ID: c.ID, Query: c.Question, Answer: answer, Citations: citations, Tools: []string{"search_docs"},
		Expected: eval.Expected{Answer: c.GroundTruth, Tools: []string{"search_docs"}, GroundedIn: []string{c.SourceDoc}},
	}
	report := eval.Build(trace, registry)
	out := ProxyScore{Pass: report.Pass, Scores: map[string]float64{}}
	for _, score := range report.Scores {
		out.Scores[score.Name] = float64(score.Value)
		if score.Name == "rag" && !score.Pass {
			out.GroundingFailure = true
		}
		if score.Name == "geval" {
			out.Quality = float64(score.Value)
		}
	}
	return out
}

func RunHarness(cases []Case, docs []rag.Document, splitter rag.Splitter) HarnessRun {
	r := &rag.Retriever{Splitter: splitter, Embedder: rag.HashEmbedder{DimV: 512}, Store: &rag.MemoryStore{}}
	r.Index(docs)
	registry := newProxyRegistry()
	out := HarnessRun{Mode: "deterministic_proxy", Cases: len(cases), Scores: map[string]float64{}}
	passed := 0
	for _, c := range cases {
		proxy := ScoreProxy(c, r.Retrieve(c.Question, 3), registry)
		if proxy.Pass {
			passed++
		}
		if proxy.GroundingFailure {
			out.GroundingFailures++
		}
		out.Quality += proxy.Quality
		for name, value := range proxy.Scores {
			out.Scores[name] += value
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
