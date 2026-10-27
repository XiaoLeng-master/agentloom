package infra

import (
	"testing"
	"time"
)

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

func TestEmbedCacheInvalidateAndTTL(t *testing.T) {
	c := NewEmbedCacheTTL(2, time.Minute)
	now := time.Now()
	c.tier.l1.now = func() time.Time { return now }
	c.Set("a", []float32{1})
	c.Set("b", []float32{2})
	c.Set("c", []float32{3})
	if _, ok := c.Get("a"); ok {
		t.Fatal("lru should have evicted a")
	}
	c.Invalidate("b")
	if _, ok := c.Get("b"); ok {
		t.Fatal("invalidated key still hits")
	}
	c.Set("fresh", []float32{4})
	now = now.Add(2 * time.Minute)
	if _, ok := c.Get("fresh"); ok {
		t.Fatal("expired key should miss")
	}
}

type memBlob struct {
	items  map[string][]byte
	hits   int
	misses int
}

func (m *memBlob) get(key string) ([]byte, bool) {
	v, ok := m.items[key]
	if ok {
		m.hits++
		return v, true
	}
	m.misses++
	return nil, false
}
func (m *memBlob) set(key string, value []byte) {
	if m.items == nil {
		m.items = map[string][]byte{}
	}
	copied := make([]byte, len(value))
	copy(copied, value)
	m.items[key] = copied
}
func (m *memBlob) del(key string)    { delete(m.items, key) }
func (m *memBlob) stats() (int, int) { return m.hits, m.misses }

func TestL2HitFillsL1(t *testing.T) {
	c := NewEmbedCache()
	l2 := &memBlob{}
	c.tier.l2 = l2
	c.Set("react", []float32{9, 8})
	c.tier.l1.del(hashString("react"))
	got, ok := c.Get("react")
	if !ok || len(got) != 2 || got[0] != 9 {
		t.Fatalf("promoted vec=%v ok=%v", got, ok)
	}
	if _, ok := c.tier.l1.get(hashString("react")); !ok {
		t.Fatal("l2 hit should fill l1")
	}
	levels := c.Levels()
	if len(levels) != 2 || !levels[1].Enabled || levels[1].Hits != 1 {
		t.Fatalf("levels=%+v", levels)
	}
}
