package rag

import "strings"

// Retriever ties a Splitter + Embedder + VectorStore together. Index splits
// documents into chunks, embeds each, and stores them. Retrieve embeds a
// query and returns the Top-K chunks. For parent-child chunks, the injected
// context is the parent (full context) rather than the matched child.
type Retriever struct {
	Splitter Splitter
	Embedder Embedder
	Store    VectorStore
}

// Index splits, embeds, and stores each document.
func (r *Retriever) Index(docs []Document) {
	for _, d := range docs {
		chunks := r.Splitter.Split(d)
		vecs := make([][]float32, len(chunks))
		for i, c := range chunks {
			vecs[i] = r.Embedder.Embed(c.Text)
		}
		r.Store.Add(chunks, vecs)
	}
}

// Context is a single retrieval result: the text to inject into the model
// prompt (parent text for parent-child chunks, chunk text otherwise) plus the
// source for citation.
type Context struct {
	Text   string
	Source string
	Score  float32
}

// Retrieve returns the Top-K contexts for the query.
func (r *Retriever) Retrieve(query string, k int) []Context {
	q := r.Embedder.Embed(query)
	hits := r.Store.Search(q, k)
	out := make([]Context, 0, len(hits))
	for _, h := range hits {
		text := h.Chunk.Text
		if h.Chunk.Parent != "" {
			text = h.Chunk.Parent
		}
		out = append(out, Context{Text: text, Source: h.Chunk.Source, Score: h.Score})
	}
	return dedupe(out) // collapse duplicates from parent-child overlaps
}

// dedupe removes contexts with identical text (parent-child can produce
// duplicates when multiple child hits share a parent).
func dedupe(cs []Context) []Context {
	seen := map[string]bool{}
	out := cs[:0]
	for _, c := range cs {
		key := strings.TrimSpace(c.Text)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}
