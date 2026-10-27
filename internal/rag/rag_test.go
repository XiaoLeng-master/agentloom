package rag

import "testing"

func TestFourSplittersProduceChunks(t *testing.T) {
	doc := Document{Source: "d", Text: "ReAct loop runs. Think then act. Observe the result. Then think again."}
	splitters := []Splitter{
		FixedSplitter{Size: 20},
		RecursiveSplitter{Size: 30, Overlap: 5},
		SemanticSplitter{},
		ParentChildSplitter{ChildSize: 10, ParentSize: 30},
	}
	for _, s := range splitters {
		cs := s.Split(doc)
		if len(cs) == 0 {
			t.Fatalf("%s produced 0 chunks", s.Name())
		}
		// each chunk must carry its source
		for _, c := range cs {
			if c.Source != "d" {
				t.Fatalf("%s: chunk source = %q", s.Name(), c.Source)
			}
		}
	}
}

func TestParentChildCarriesParent(t *testing.T) {
	s := ParentChildSplitter{ChildSize: 10, ParentSize: 30}
	cs := s.Split(Document{Source: "d", Text: "abcdefghijklmnopqrstuvwxyz0123456789"})
	for _, c := range cs {
		if c.Parent == "" {
			t.Fatalf("parent-child chunk should carry parent text")
		}
	}
}

func TestRetrieverReturnsRelevant(t *testing.T) {
	r := &Retriever{
		Splitter: SemanticSplitter{},
		Embedder: HashEmbedder{DimV: 256},
		Store:    &MemoryStore{},
	}
	r.Index([]Document{
		{Source: "go", Text: "The ReAct loop alternates thinking acting observing."},
		{Source: "py", Text: "Python async uses coroutines and event loops."},
		{Source: "db", Text: "PostgreSQL MVCC allows concurrent reads and writes."},
	})
	got := r.Retrieve("how does the ReAct loop work", 2)
	if len(got) == 0 {
		t.Fatal("no results")
	}
	if got[0].Source != "go" {
		t.Fatalf("top hit should be go doc, got %q (score=%.3f)", got[0].Source, got[0].Score)
	}
}

type countingEmbedder struct {
	inner Embedder
	texts []string
}

func (c *countingEmbedder) Dim() int { return c.inner.Dim() }
func (c *countingEmbedder) Embed(text string) []float32 {
	c.texts = append(c.texts, text)
	return c.inner.Embed(text)
}

func TestSyncReembedsOnlyChangedDocument(t *testing.T) {
	embedder := &countingEmbedder{inner: HashEmbedder{DimV: 64}}
	cache := &mapCache{items: map[string][]float32{}}
	r := &Retriever{
		Splitter: FixedSplitter{Size: 20},
		Embedder: embedder,
		Store:    &MemoryStore{},
		Cache:    cache,
	}
	alpha := Document{Source: "alpha", Text: "alpha react loop think act observe"}
	beta := Document{Source: "beta", Text: "beta redis ttl lru memory vector"}
	r.Sync([]Document{alpha})
	alphaChunks := append([]string(nil), embedder.texts...)
	embedder.texts = nil
	r.Sync([]Document{alpha, beta})
	betaChunks := append([]string(nil), embedder.texts...)
	if len(betaChunks) == 0 {
		t.Fatal("beta was not embedded on first sight")
	}
	embedder.texts = nil
	alpha.Text = "zzzz changed alpha document only"
	events := r.Sync([]Document{alpha, beta})
	if len(embedder.texts) == 0 {
		t.Fatal("changed document was not embedded")
	}
	for _, text := range embedder.texts {
		for _, old := range betaChunks {
			if text == old {
				t.Fatalf("unchanged beta chunk embedded again: %q", text)
			}
		}
	}
	got := map[string]string{}
	for _, event := range events {
		got[event.Source] = event.Action
	}
	if got["alpha"] != EventUpdated || got["beta"] != EventUnchanged {
		t.Fatalf("events=%v", got)
	}
	for _, text := range betaChunks {
		if _, ok := cache.Get(text); !ok {
			t.Fatalf("unchanged chunk missing from cache: %q", text)
		}
	}
	for _, text := range alphaChunks {
		if _, ok := cache.Get(text); ok {
			t.Fatalf("old alpha chunk still cached: %q", text)
		}
	}
	hits := r.Retrieve("beta redis", 1)
	if len(hits) == 0 || hits[0].Source != "beta" {
		t.Fatalf("beta should still be searchable, hits=%v", hits)
	}
}

type mapCache struct{ items map[string][]float32 }

func (m *mapCache) Get(text string) ([]float32, bool) {
	v, ok := m.items[text]
	return v, ok
}
func (m *mapCache) Set(text string, vec []float32) { m.items[text] = append([]float32(nil), vec...) }
func (m *mapCache) Invalidate(text string)         { delete(m.items, text) }

func TestRetrieverDedupesParentChild(t *testing.T) {
	r := &Retriever{
		Splitter: ParentChildSplitter{ChildSize: 5, ParentSize: 20},
		Embedder: HashEmbedder{DimV: 128},
		Store:    &MemoryStore{},
	}
	r.Index([]Document{{Source: "d", Text: "react think act observe loop model tool"}})
	got := r.Retrieve("react loop", 5)
	// multiple child hits may share a parent; dedupe must collapse them.
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c.Text] {
			t.Fatalf("duplicate context returned: %q", c.Text)
		}
		seen[c.Text] = true
	}
}
