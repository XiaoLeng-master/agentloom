package app

import (
	"context"

	"github.com/XiaoLeng-master/agentloom/internal/core"
	"github.com/XiaoLeng-master/agentloom/internal/infra"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

// CacheSavings is one local comparison of embed and LLM calls.
// The embedder is HashEmbedder. The LLM is an in-process counter, not a paid API.
type CacheSavings struct {
	Requests          int     `json:"requests"`
	EmbedWithoutCache int     `json:"embed_without_cache"`
	EmbedWithCache    int     `json:"embed_with_cache"`
	LLMWithoutCache   int     `json:"llm_without_cache"`
	LLMWithCache      int     `json:"llm_with_cache"`
	EmbedL1Hits       int     `json:"embed_l1_hits"`
	EmbedL1Misses     int     `json:"embed_l1_misses"`
	EmbedL1HitRate    float64 `json:"embed_l1_hit_rate"`
	PrefixL1Hits      int     `json:"prefix_l1_hits"`
	PrefixL1Misses    int     `json:"prefix_l1_misses"`
	PrefixL1HitRate   float64 `json:"prefix_l1_hit_rate"`
}

type callEmbedder struct {
	n     int
	inner rag.HashEmbedder
}

func (c *callEmbedder) Dim() int { return c.inner.Dim() }
func (c *callEmbedder) Embed(text string) []float32 {
	c.n++
	return c.inner.Embed(text)
}

type callLLM struct{ n int }

func (c *callLLM) Complete(context.Context, []core.Message) (core.Message, error) {
	c.n++
	return core.Message{Role: core.RoleAssistant, Content: "local-answer"}, nil
}

// MeasureCacheSavings runs the same documents, queries, and prompts twice.
func MeasureCacheSavings() CacheSavings {
	docs := []rag.Document{
		{Source: "alpha", Text: "react loop think act observe tool result planner executor verifier"},
		{Source: "beta", Text: "memory lru ttl redis cache embed prefix hash vector search"},
	}
	queries := []string{"react loop tool", "lru ttl cache"}
	const requests = 4
	out := CacheSavings{Requests: requests}
	for pass := 0; pass < 2; pass++ {
		embedder := &callEmbedder{inner: rag.HashEmbedder{DimV: 128}}
		retriever := &rag.Retriever{Splitter: rag.FixedSplitter{Size: 40}, Embedder: embedder, Store: &rag.MemoryStore{}}
		retriever.Index(docs)
		for _, query := range queries {
			retriever.Retrieve(query, 3)
		}
		out.EmbedWithoutCache += embedder.n
	}
	embedder := &callEmbedder{inner: rag.HashEmbedder{DimV: 128}}
	cache := infra.NewEmbedCache()
	retriever := &rag.Retriever{Splitter: rag.FixedSplitter{Size: 40}, Embedder: embedder, Store: &rag.MemoryStore{}, Cache: cache}
	for pass := 0; pass < 2; pass++ {
		retriever.Index(docs)
		for _, query := range queries {
			retriever.Retrieve(query, 3)
		}
	}
	out.EmbedWithCache = embedder.n
	out.EmbedL1Hits, out.EmbedL1Misses = cache.Stats()
	if total := out.EmbedL1Hits + out.EmbedL1Misses; total > 0 {
		out.EmbedL1HitRate = float64(out.EmbedL1Hits) / float64(total)
	}
	plain := &callLLM{}
	msgs := []core.Message{{Role: core.RoleSystem, Content: "local system"}, {Role: core.RoleUser, Content: "same question"}}
	for i := 0; i < requests; i++ {
		_, _ = plain.Complete(context.Background(), msgs)
	}
	out.LLMWithoutCache = plain.n
	inner := &callLLM{}
	prefix := infra.NewPrefixCache()
	cached := core.NewCachedLLM(inner, prefix)
	for i := 0; i < requests; i++ {
		_, _ = cached.Complete(context.Background(), msgs)
	}
	out.LLMWithCache = inner.n
	out.PrefixL1Hits, out.PrefixL1Misses = prefix.Stats()
	if total := out.PrefixL1Hits + out.PrefixL1Misses; total > 0 {
		out.PrefixL1HitRate = float64(out.PrefixL1Hits) / float64(total)
	}
	return out
}
