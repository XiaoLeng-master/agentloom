package infra

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultEmbedCap  = 4096
	defaultPrefixCap = 1024
	embedKeyPrefix   = "agentloom:cache:embed:"
	prefixKeyPrefix  = "agentloom:cache:prefix:"
)

// CacheLevel is one tier of a cache. HitRate is hits / (hits + misses), or 0
// when that tier has not been read.
type CacheLevel struct {
	Level   string  `json:"level"`
	Enabled bool    `json:"enabled"`
	Hits    int     `json:"hits"`
	Misses  int     `json:"misses"`
	HitRate float64 `json:"hit_rate"`
}

// PrefixCache memoizes a full completion for an identical message prefix.
// L1 is an in-memory LRU. L2 is Redis and stays disabled until UseRedis.
// This is not a provider prompt cache: a hit skips the model call entirely.
type PrefixCache struct{ tier *tier }

// NewPrefixCache returns an L1 cache with no expiry.
func NewPrefixCache() *PrefixCache {
	return NewPrefixCacheTTL(defaultPrefixCap, 0)
}

// NewPrefixCacheTTL returns an L1 cache. ttl <= 0 means entries do not expire.
func NewPrefixCacheTTL(capacity int, ttl time.Duration) *PrefixCache {
	return &PrefixCache{tier: newTier(capacity, ttl)}
}

// UseRedis enables L2. An empty addr leaves L2 disabled.
func (c *PrefixCache) UseRedis(addr string, ttl time.Duration) {
	c.tier.useRedis(addr, prefixKeyPrefix, ttl)
}

// Get returns (result, true) on a prefix-hash hit.
func (c *PrefixCache) Get(prefix []byte) (string, bool) {
	raw, ok := c.tier.get(hashBytes(prefix))
	if !ok {
		return "", false
	}
	return string(raw), true
}

// Set stores a result keyed by prefix hash.
func (c *PrefixCache) Set(prefix []byte, result string) {
	c.tier.set(hashBytes(prefix), []byte(result))
}

// Invalidate drops one prefix from every enabled tier.
func (c *PrefixCache) Invalidate(prefix []byte) { c.tier.del(hashBytes(prefix)) }

// Stats returns L1 hits and misses. Per-tier rates are in Levels.
func (c *PrefixCache) Stats() (hits, misses int) { return c.tier.l1.stats() }

// Levels reports L1 and L2 separately.
func (c *PrefixCache) Levels() []CacheLevel { return c.tier.levels() }

// EmbedCache memoizes embedding vectors by text hash.
// L1 is an in-memory LRU. L2 is Redis and stays disabled until UseRedis.
type EmbedCache struct{ tier *tier }

// NewEmbedCache returns an L1 cache with no expiry.
func NewEmbedCache() *EmbedCache {
	return NewEmbedCacheTTL(defaultEmbedCap, 0)
}

// NewEmbedCacheTTL returns an L1 cache. ttl <= 0 means entries do not expire.
func NewEmbedCacheTTL(capacity int, ttl time.Duration) *EmbedCache {
	return &EmbedCache{tier: newTier(capacity, ttl)}
}

// UseRedis enables L2. An empty addr leaves L2 disabled.
func (c *EmbedCache) UseRedis(addr string, ttl time.Duration) {
	c.tier.useRedis(addr, embedKeyPrefix, ttl)
}

// Get returns a copy of the vector on a text-hash hit.
func (c *EmbedCache) Get(text string) ([]float32, bool) {
	raw, ok := c.tier.get(hashString(text))
	if !ok {
		return nil, false
	}
	return decodeVec(raw), true
}

// Set stores a copy of the vector keyed by text hash.
func (c *EmbedCache) Set(text string, v []float32) {
	c.tier.set(hashString(text), encodeVec(v))
}

// Invalidate drops one text from every enabled tier.
func (c *EmbedCache) Invalidate(text string) { c.tier.del(hashString(text)) }

// Stats returns L1 hits and misses. Per-tier rates are in Levels.
func (c *EmbedCache) Stats() (hits, misses int) { return c.tier.l1.stats() }

// Levels reports L1 and L2 separately.
func (c *EmbedCache) Levels() []CacheLevel { return c.tier.levels() }

type blobCache interface {
	get(key string) ([]byte, bool)
	set(key string, value []byte)
	del(key string)
	stats() (hits, misses int)
}

type tier struct {
	l1  *memoryLRU
	ttl time.Duration
	mu  sync.RWMutex
	l2  blobCache
}

func newTier(capacity int, ttl time.Duration) *tier {
	if capacity < 1 {
		capacity = 1
	}
	return &tier{l1: newMemoryLRU(capacity, ttl), ttl: ttl}
}

func (t *tier) useRedis(addr, keyPrefix string, ttl time.Duration) {
	if addr == "" {
		return
	}
	if ttl <= 0 {
		ttl = t.ttl
	}
	t.mu.Lock()
	t.l2 = newRedisCache(addr, keyPrefix, ttl)
	t.mu.Unlock()
}

func (t *tier) get(key string) ([]byte, bool) {
	if raw, ok := t.l1.get(key); ok {
		return raw, true
	}
	t.mu.RLock()
	l2 := t.l2
	t.mu.RUnlock()
	if l2 == nil {
		return nil, false
	}
	raw, ok := l2.get(key)
	if !ok {
		return nil, false
	}
	t.l1.set(key, raw)
	out := make([]byte, len(raw))
	copy(out, raw)
	return out, true
}

func (t *tier) set(key string, value []byte) {
	t.l1.set(key, value)
	t.mu.RLock()
	l2 := t.l2
	t.mu.RUnlock()
	if l2 != nil {
		l2.set(key, value)
	}
}

func (t *tier) del(key string) {
	t.l1.del(key)
	t.mu.RLock()
	l2 := t.l2
	t.mu.RUnlock()
	if l2 != nil {
		l2.del(key)
	}
}

func (t *tier) levels() []CacheLevel {
	h, m := t.l1.stats()
	out := []CacheLevel{levelReport("l1", true, h, m)}
	t.mu.RLock()
	l2 := t.l2
	t.mu.RUnlock()
	if l2 == nil {
		out = append(out, levelReport("l2", false, 0, 0))
		return out
	}
	h, m = l2.stats()
	out = append(out, levelReport("l2", true, h, m))
	return out
}

func levelReport(name string, enabled bool, hits, misses int) CacheLevel {
	rate := 0.0
	if hits+misses > 0 {
		rate = float64(hits) / float64(hits+misses)
	}
	return CacheLevel{Level: name, Enabled: enabled, Hits: hits, Misses: misses, HitRate: rate}
}

type memoryItem struct {
	key     string
	value   []byte
	expires time.Time
}

type memoryLRU struct {
	mu     sync.Mutex
	cap    int
	ttl    time.Duration
	ll     *list.List
	idx    map[string]*list.Element
	hits   int
	misses int
	now    func() time.Time
}

func newMemoryLRU(capacity int, ttl time.Duration) *memoryLRU {
	return &memoryLRU{cap: capacity, ttl: ttl, ll: list.New(), idx: map[string]*list.Element{}, now: time.Now}
}

func (m *memoryLRU) get(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.idx[key]
	if !ok {
		m.misses++
		return nil, false
	}
	item := el.Value.(*memoryItem)
	if !item.expires.IsZero() && !m.now().Before(item.expires) {
		m.ll.Remove(el)
		delete(m.idx, key)
		m.misses++
		return nil, false
	}
	m.ll.MoveToFront(el)
	m.hits++
	out := make([]byte, len(item.value))
	copy(out, item.value)
	return out, true
}

func (m *memoryLRU) set(key string, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := make([]byte, len(value))
	copy(copied, value)
	var expires time.Time
	if m.ttl > 0 {
		expires = m.now().Add(m.ttl)
	}
	if el, ok := m.idx[key]; ok {
		item := el.Value.(*memoryItem)
		item.value = copied
		item.expires = expires
		m.ll.MoveToFront(el)
		return
	}
	el := m.ll.PushFront(&memoryItem{key: key, value: copied, expires: expires})
	m.idx[key] = el
	for m.ll.Len() > m.cap {
		back := m.ll.Back()
		m.ll.Remove(back)
		delete(m.idx, back.Value.(*memoryItem).key)
	}
}

func (m *memoryLRU) del(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.idx[key]
	if !ok {
		return
	}
	m.ll.Remove(el)
	delete(m.idx, key)
}

func (m *memoryLRU) stats() (hits, misses int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits, m.misses
}

type redisCache struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
	hits   atomic.Int64
	misses atomic.Int64
}

func newRedisCache(addr, prefix string, ttl time.Duration) *redisCache {
	return &redisCache{client: redis.NewClient(&redis.Options{Addr: addr}), prefix: prefix, ttl: ttl}
}

func (c *redisCache) get(key string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	raw, err := c.client.Get(ctx, c.prefix+key).Bytes()
	if err != nil {
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	return raw, true
}

func (c *redisCache) set(key string, value []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = c.client.Set(ctx, c.prefix+key, value, c.ttl).Err()
}

func (c *redisCache) del(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = c.client.Del(ctx, c.prefix+key).Err()
}

func (c *redisCache) stats() (hits, misses int) {
	return int(c.hits.Load()), int(c.misses.Load())
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashString(s string) string { return hashBytes([]byte(s)) }

func encodeVec(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

func decodeVec(buf []byte) []float32 {
	if len(buf)%4 != 0 {
		return nil
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out
}
