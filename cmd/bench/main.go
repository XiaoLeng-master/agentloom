// Command bench runs the RAG chunking-strategy benchmark against
// datasets/rag_eval_98.json and prints a HitRate@K / MRR@K comparison table.
//
// The embedder is the dependency-free HashEmbedder (lexical overlap). It is NOT
// semantic — it makes HitRate differences between strategies attributable to
// chunking rather than to embedder noise. To get production numbers, swap in
// nomic-embed-text via Ollama; the methodology is identical.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

type evalCase struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Question    string `json:"question"`
	GroundTruth string `json:"groundTruth"`
	SourceDoc   string `json:"sourceDoc"`
}

func main() {
	data, err := os.ReadFile("datasets/rag_eval_98.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read dataset: %v\n", err)
		os.Exit(1)
	}
	var cases []evalCase
	if err := json.Unmarshal(data, &cases); err != nil {
		fmt.Fprintf(os.Stderr, "parse dataset: %v\n", err)
		os.Exit(1)
	}
	if len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "empty dataset")
		os.Exit(1)
	}

	// Build the corpus from the REAL public files each case references
	// (README, design docs, source files). A retrieval "hit" is when the
	// top-K contexts contain a chunk whose source matches the case's
	// sourceDoc. Indexing real files (not the groundTruths themselves) is
	// what makes HitRate differ between strategies: a strategy that splits
	// the answer across a chunk boundary misses; one that keeps it whole hits.
	seen := map[string]bool{}
	var documents []rag.Document
	for _, c := range cases {
		if seen[c.SourceDoc] {
			continue
		}
		seen[c.SourceDoc] = true
		text, err := os.ReadFile(c.SourceDoc)
		if err != nil {
			// skip missing files (e.g. source not yet committed)
			continue
		}
		documents = append(documents, rag.Document{Source: c.SourceDoc, Text: string(text)})
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Source < documents[j].Source })

	splitters := []rag.Splitter{
		rag.FixedSplitter{Size: 40},
		rag.RecursiveSplitter{Size: 60, Overlap: 10},
		rag.SemanticSplitter{},
		rag.ParentChildSplitter{ChildSize: 20, ParentSize: 80},
	}
	const K = 3

	fmt.Printf("# agentloom RAG chunking benchmark\n\n")
	fmt.Printf("- dataset: datasets/rag_eval_98.json (%d cases)\n", len(cases))
	fmt.Printf("- embedder: HashEmbedder (lexical baseline; real numbers need Ollama nomic-embed-text)\n")
	fmt.Printf("- K = %d, hit = top-K contains a chunk whose source matches the case's sourceDoc\n\n", K)
	fmt.Printf("| 切分策略 | HitRate@K | MRR@K |\n|---|---|---|\n")

	for _, s := range splitters {
		r := &rag.Retriever{Splitter: s, Embedder: rag.HashEmbedder{DimV: 512}, Store: &rag.MemoryStore{}}
		r.Index(documents)
		hits, mrr := 0, 0.0
		for _, c := range cases {
			ctxs := r.Retrieve(c.Question, K)
			for rank, ctx := range ctxs {
				if ctx.Source == c.SourceDoc {
					hits++
					mrr += 1.0 / float64(rank+1)
					break
				}
			}
		}
		hitRate := float64(hits) / float64(len(cases)) * 100
		mrrScore := mrr / float64(len(cases)) * 100
		fmt.Printf("| %s | %.1f%% | %.1f%% |\n", s.Name(), hitRate, mrrScore)
	}
}
