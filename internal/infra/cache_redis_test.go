//go:build redis

package infra

import (
	"os"
	"testing"
	"time"
)

func TestEmbedCacheRedisRoundTrip(t *testing.T) {
	addr := os.Getenv("AGENTLOOM_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("AGENTLOOM_TEST_REDIS_ADDR unset; not running against Redis")
	}
	cache := NewEmbedCacheTTL(8, time.Minute)
	cache.UseRedis(addr, time.Minute)
	cache.Set("react loop", []float32{1, 2, 3})
	other := NewEmbedCacheTTL(8, time.Minute)
	other.UseRedis(addr, time.Minute)
	got, ok := other.Get("react loop")
	if !ok || len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("l2 round trip got %v ok=%v", got, ok)
	}
	levels := other.Levels()
	if len(levels) != 2 || !levels[1].Enabled || levels[1].Hits < 1 {
		t.Fatalf("levels=%+v", levels)
	}
	cache.Invalidate("react loop")
	if _, ok := other.Get("react loop"); ok {
		t.Fatal("invalidated text still present")
	}
}
