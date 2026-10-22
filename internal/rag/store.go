package rag

// VectorStore holds chunk vectors and returns Top-K by cosine similarity.
// MemoryStore is the in-process impl; production swaps in Milvus. The
// interface keeps the Retriever agnostic to where vectors live.
type VectorStore interface {
	Add(chunks []Chunk, vecs [][]float32)
	Search(q []float32, k int) []ScoredChunk
}

// ScoredChunk pairs a chunk with its similarity score.
type ScoredChunk struct {
	Chunk Chunk
	Score float32
}

type storeItem struct {
	chunk Chunk
	vec   []float32
}

// MemoryStore is a simple in-memory vector store with linear-scan Top-K.
type MemoryStore struct {
	items []storeItem
}

func (s *MemoryStore) Add(chunks []Chunk, vecs [][]float32) {
	for i, c := range chunks {
		if i < len(vecs) {
			s.items = append(s.items, storeItem{chunk: c, vec: vecs[i]})
		}
	}
}

func (s *MemoryStore) Search(q []float32, k int) []ScoredChunk {
	scored := make([]ScoredChunk, 0, len(s.items))
	for _, it := range s.items {
		scored = append(scored, ScoredChunk{Chunk: it.chunk, Score: cosine(q, it.vec)})
	}
	// partial selection of top-k (simple: sort then slice; fine for offline sizes).
	for i := 0; i < len(scored); i++ {
		max := i
		for j := i + 1; j < len(scored); j++ {
			if scored[j].Score > scored[max].Score {
				max = j
			}
		}
		scored[i], scored[max] = scored[max], scored[i]
		if i+1 >= k {
			break
		}
	}
	if k > len(scored) {
		k = len(scored)
	}
	return scored[:k]
}
