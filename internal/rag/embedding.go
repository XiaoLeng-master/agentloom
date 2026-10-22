package rag

import (
	"strings"
)

// Embedder turns text into a fixed-dim vector. HashEmbedder below is a
// deterministic, dependency-free embedder: each token is hashed to a bucket
// and the bucket count forms a sparse vector. Real deployments swap in
// nomic-embed-text via Ollama; the retrieval methodology is identical.
type Embedder interface {
	Embed(text string) []float32
	Dim() int
}

// HashEmbedder is a toy embedder used for tests and the offline benchmark
// baseline. It is NOT semantic — it captures lexical overlap — which is
// exactly what makes HitRate@K differences between chunking strategies
// attributable to chunking rather than to embedder noise.
type HashEmbedder struct{ DimV int }

func (e HashEmbedder) Dim() int { return e.DimV }
func (e HashEmbedder) Embed(text string) []float32 {
	v := make([]float32, e.DimV)
	for _, tok := range strings.Fields(strings.ToLower(text)) {
		h := fnv1a(tok) % uint32(e.DimV)
		v[h] += 1
	}
	return v
}

func fnv1a(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// cosine returns the cosine similarity between two vectors.
func cosine(a, b []float32) float32 {
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt32(na) * sqrt32(nb))
}

func sqrt32(x float32) float32 {
	if x <= 0 {
		return 0
	}
	// one Newton step from a float approximation; good enough for ranking.
	r := float32(1)
	for i := 0; i < 20; i++ {
		r = (r + x/r) * 0.5
	}
	return r
}
