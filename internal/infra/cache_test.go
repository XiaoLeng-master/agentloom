package infra

import "testing"

func TestPrefixCache_HitMiss(t *testing.T) {
	c := NewPrefixCache()
	prefix := []byte("system: you are helpful\nuser: hi")
	if _, ok := c.Get(prefix); ok {
		t.Fatal("first Get should miss")
	}
	c.Set(prefix, "answer")
	got, ok := c.Get(prefix)
	if !ok || got != "answer" {
		t.Fatalf("second Get should hit with answer, got %q ok=%v", got, ok)
	}
	// different prefix misses
	if _, ok := c.Get([]byte("different")); ok {
		t.Fatal("different prefix should miss")
	}
	hits, miss := c.Stats()
	if hits != 1 || miss != 2 {
		t.Fatalf("stats: hits=%d miss=%d, want 1/2", hits, miss)
	}
}

func TestEmbedCache_Dedup(t *testing.T) {
	c := NewEmbedCache()
	txt := "ReAct loop think act observe"
	if _, ok := c.Get(txt); ok {
		t.Fatal("first embed should miss")
	}
	c.Set(txt, []float32{1, 2, 3})
	v, ok := c.Get(txt)
	if !ok || len(v) != 3 {
		t.Fatalf("second embed should hit, got ok=%v len=%d", ok, len(v))
	}
	// same text again still hits (no re-embed needed)
	_, ok = c.Get(txt)
	if !ok {
		t.Fatal("same text must hit repeatedly")
	}
	hits, miss := c.Stats()
	if hits != 2 || miss != 1 {
		t.Fatalf("stats: hits=%d miss=%d, want 2/1", hits, miss)
	}
}
