package rag

import (
	"testing"
)

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
