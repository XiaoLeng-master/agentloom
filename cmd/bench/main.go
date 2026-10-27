// Command bench runs the RAG chunking-strategy benchmark against
// datasets/rag_eval_98.json and prints source-hit and lexical-proxy columns.
//
// The embedder is the dependency-free HashEmbedder (lexical overlap). It is NOT
// semantic — it makes HitRate differences between strategies attributable to
// chunking rather than to embedder noise. The proxy column is the same
// deterministic lexical score as GET /v1/bench. It does not call a model API.
package main

import (
	"fmt"
	"os"

	"github.com/XiaoLeng-master/agentloom/internal/evolution"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

func main() {
	cases, documents, _, err := evolution.LoadPublicCorpus("datasets/rag_eval_98.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load corpus: %v\n", err)
		os.Exit(1)
	}

	splitters := []rag.Splitter{
		rag.FixedSplitter{Size: 40},
		rag.RecursiveSplitter{Size: 60, Overlap: 10},
		rag.SemanticSplitter{},
		rag.ParentChildSplitter{ChildSize: 20, ParentSize: 80},
	}
	const K = 3

	fmt.Printf("# agentloom RAG chunking benchmark\n\n")
	fmt.Printf("- dataset: datasets/rag_eval_98.json (%d cases)\n", len(cases))
	fmt.Printf("- embedder: HashEmbedder (lexical). No paid API and no live model answer.\n")
	fmt.Printf("- K = %d, 来源命中 = top-K contains a chunk whose source matches sourceDoc\n", K)
	fmt.Printf("- 确定性代理分数 = mean normalized lexical GEval on the first retrieved chunk (0–1). 真实回答未评.\n\n")
	fmt.Printf("| 切分策略 | 来源命中 HitRate@3 | MRR@3 | 确定性代理分数 | 通过率 | 引用失败 |\n|---|---|---|---|---|---|\n")

	for _, s := range splitters {
		run := evolution.AttachHarness(evolution.RunRetrieval(cases, documents, "", s), cases, documents, s)
		fmt.Printf("| %s | %.1f%% | %.1f%% | %.4f | %.1f%% | %d |\n",
			s.Name(), run.HitRate*100, mrr(run)*100, run.Quality, run.PassRate*100, run.GroundingFailures)
	}
}

func mrr(run evolution.RetrievalRun) float64 {
	if len(run.Results) == 0 {
		return 0
	}
	sum := 0.0
	for _, result := range run.Results {
		for rank, source := range result.RetrievedSources {
			if source == result.ExpectedSource {
				sum += 1.0 / float64(rank+1)
				break
			}
		}
	}
	return sum / float64(len(run.Results))
}
