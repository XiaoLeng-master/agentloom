// Package infra provides cross-cutting infrastructure for the agent runtime.
// Currently: prefix caching for LLM completions and embeddings, inspired by
// Pi's observation that when the system prompt, tool definitions, and
// conversation prefix are unchanged, the model's prefix computation can be
// reused — cutting repeated-call cost dramatically (Pi reports ~99.9% prefix
// cache hit in coding-agent workloads).
package infra

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// PrefixCache memoizes LLM completion results keyed by the hash of the message
// prefix (system prompt + messages up to the last turn). A hit returns the
// prior result without re-invoking the model. This is the Pi-style prefix
// cache: same prefix => same prefix KV work is reused.
type PrefixCache struct {
	mu    sync.RWMutex
	items map[string]string
	hits  int
	miss  int
}

// NewPrefixCache returns an empty prefix cache.
func NewPrefixCache() *PrefixCache {
	return &PrefixCache{items: map[string]string{}}
}

// Get returns (result, true) on a prefix-hash hit.
func (c *PrefixCache) Get(prefix []byte) (string, bool) {
	k := hashBytes(prefix)
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[k]
	if ok {
		c.hits++
	} else {
		c.miss++
	}
	return v, ok
}

// Set stores a result keyed by prefix hash.
func (c *PrefixCache) Set(prefix []byte, result string) {
	k := hashBytes(prefix)
	c.mu.Lock()
	c.items[k] = result
	c.mu.Unlock()
}

// Stats returns (hits, misses) for observability.
func (c *PrefixCache) Stats() (hits, misses int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.miss
}

// EmbedCache memoizes embedding vectors by text hash. Same text => same vector,
// skipping re-embedding on repeated queries (a major cost saver in RAG where
// the same document chunks and hot queries get embedded over and over).
type EmbedCache struct {
	mu    sync.RWMutex
	items map[string][]float32
	hits  int
	miss  int
}

// NewEmbedCache returns an empty embedding cache.
func NewEmbedCache() *EmbedCache {
	return &EmbedCache{items: map[string][]float32{}}
}

// Get returns (vec, true) on a text-hash hit.
func (c *EmbedCache) Get(text string) ([]float32, bool) {
	k := hashString(text)
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[k]
	if ok {
		c.hits++
	} else {
		c.miss++
	}
	return v, ok
}

// Set stores a vector keyed by text hash.
func (c *EmbedCache) Set(text string, v []float32) {
	k := hashString(text)
	c.mu.Lock()
	c.items[k] = v
	c.mu.Unlock()
}

// Stats returns (hits, misses).
func (c *EmbedCache) Stats() (hits, misses int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.hits, c.miss
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashString(s string) string { return hashBytes([]byte(s)) }
