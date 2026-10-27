package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// TextCache stores vectors for exact text. A nil cache embeds every time.
type TextCache interface {
	Get(text string) ([]float32, bool)
	Set(text string, vec []float32)
	Invalidate(text string)
}

// Retriever ties a Splitter + Embedder + VectorStore together. Index splits
// documents into chunks, embeds each, and stores them. Retrieve embeds a
// query and returns the Top-K chunks. For parent-child chunks, the injected
// context is the parent (full context) rather than the matched child.
type Retriever struct {
	Splitter Splitter
	Embedder Embedder
	Store    VectorStore
	Cache    TextCache
	indexed  map[string]indexedDoc
}

type indexedDoc struct {
	hash   string
	chunks []string
}

// CorpusEvent is emitted when Sync sees a document hash change.
type CorpusEvent struct {
	Source string `json:"source"`
	Action string `json:"action"`
	Hash   string `json:"hash"`
}

const (
	EventCreated   = "created"
	EventUpdated   = "updated"
	EventUnchanged = "unchanged"
	EventRemoved   = "removed"
)

// Index records each document hash and embeds only sources whose text changed.
func (r *Retriever) Index(docs []Document) {
	r.Sync(docs)
}

// Sync re-splits and re-embeds only documents whose text hash changed.
// Chunk texts from a changed or removed document are dropped from Cache.
func (r *Retriever) Sync(docs []Document) []CorpusEvent {
	if r.indexed == nil {
		r.indexed = map[string]indexedDoc{}
	}
	next := map[string]Document{}
	for _, doc := range docs {
		next[doc.Source] = doc
	}
	var sources []string
	seen := map[string]bool{}
	for source := range r.indexed {
		sources = append(sources, source)
		seen[source] = true
	}
	for source := range next {
		if !seen[source] {
			sources = append(sources, source)
		}
	}
	sort.Strings(sources)
	var events []CorpusEvent
	for _, source := range sources {
		doc, ok := next[source]
		prev, had := r.indexed[source]
		if !ok {
			r.dropSource(source, prev)
			delete(r.indexed, source)
			events = append(events, CorpusEvent{Source: source, Action: EventRemoved, Hash: prev.hash})
			continue
		}
		hash := hashText(doc.Text)
		if had && prev.hash == hash {
			events = append(events, CorpusEvent{Source: source, Action: EventUnchanged, Hash: hash})
			continue
		}
		if had {
			r.dropSource(source, prev)
		}
		record := r.storeNew(doc)
		r.indexed[source] = record
		action := EventCreated
		if had {
			action = EventUpdated
		}
		events = append(events, CorpusEvent{Source: source, Action: action, Hash: hash})
	}
	return events
}

func (r *Retriever) storeNew(doc Document) indexedDoc {
	chunks := r.Splitter.Split(doc)
	vecs := make([][]float32, len(chunks))
	texts := make([]string, len(chunks))
	for i, chunk := range chunks {
		texts[i] = chunk.Text
		vecs[i] = r.embed(chunk.Text)
	}
	r.Store.Add(chunks, vecs)
	return indexedDoc{hash: hashText(doc.Text), chunks: texts}
}

func (r *Retriever) dropSource(source string, prev indexedDoc) {
	if src, ok := r.Store.(interface{ RemoveSource(string) }); ok {
		src.RemoveSource(source)
	}
	if r.Cache == nil {
		return
	}
	for _, text := range prev.chunks {
		r.Cache.Invalidate(text)
	}
}

func (r *Retriever) embed(text string) []float32 {
	if r.Cache != nil {
		if vec, ok := r.Cache.Get(text); ok {
			return vec
		}
	}
	vec := r.Embedder.Embed(text)
	if r.Cache != nil {
		r.Cache.Set(text, vec)
	}
	return vec
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
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
	q := r.embed(query)
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
