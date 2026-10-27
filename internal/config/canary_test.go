package config

import (
	"fmt"
	"testing"
	"time"
)

func TestCanarySplitRatioIsExactAndSticky(t *testing.T) {
	found := map[int]string{}
	for i := 0; len(found) < 100; i++ {
		id := fmt.Sprintf("tenant-%d", i)
		bucket := TenantBucket(id)
		if _, ok := found[bucket]; !ok {
			found[bucket] = id
		}
	}
	for percent := 0; percent <= 100; percent++ {
		hits := 0
		for bucket := 0; bucket < 100; bucket++ {
			if InCanary(found[bucket], percent) {
				hits++
			}
		}
		if hits != percent {
			t.Fatalf("percent=%d hits=%d", percent, hits)
		}
	}
	tenant := found[3]
	if InCanary(tenant, 3) || !InCanary(tenant, 4) || !InCanary(tenant, 50) || !InCanary(tenant, 100) {
		t.Fatal("widening the percent dropped a tenant that was already inside")
	}
	if InCanary(tenant, 4) != InCanary(tenant, 4) {
		t.Fatal("bucket was not sticky")
	}
}

func TestCanaryRollbackKeepsInFlightConfig(t *testing.T) {
	m := NewManager()
	m.Register("kb-agent", Config{Name: "kb-agent", SystemPrompt: "stable"})
	canaryTenant, ok := TenantForBucket(0)
	if !ok {
		t.Fatal("missing canary tenant")
	}
	stableTenant, ok := TenantForBucket(99)
	if !ok {
		t.Fatal("missing stable tenant")
	}
	version, err := m.StartCanary("kb-agent", Config{Name: "kb-agent", SystemPrompt: "canary", ChunkStrategy: "recursive"}, 10)
	if err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	held, err := m.AcquireTenant("kb-agent", canaryTenant)
	if err != nil {
		t.Fatal(err)
	}
	if held.Config().SystemPrompt != "canary" || held.Config().Version != 2 {
		t.Fatalf("held=%+v", held.Config())
	}
	outside, err := m.AcquireTenant("kb-agent", stableTenant)
	if err != nil {
		t.Fatal(err)
	}
	if outside.Config().Version != 1 || outside.Config().SystemPrompt != "stable" {
		t.Fatalf("outside=%+v", outside.Config())
	}
	outside.Release()
	plain, err := m.Acquire("kb-agent")
	if err != nil || plain.Config().Version != 1 {
		t.Fatalf("plain acquire changed during canary: %+v err=%v", plain.Config(), err)
	}
	plain.Release()

	if err := m.RollbackCanary("kb-agent"); err != nil {
		t.Fatal(err)
	}
	if held.Config().SystemPrompt != "canary" || held.Config().Version != 2 {
		t.Fatalf("in-flight config changed after rollback: %+v", held.Config())
	}
	if m.Drain("kb-agent", 30*time.Millisecond) == 0 {
		t.Fatal("drain finished while the canary request was still held")
	}
	next, err := m.AcquireTenant("kb-agent", canaryTenant)
	if err != nil {
		t.Fatal(err)
	}
	if next.Config().Version != 1 || next.Config().SystemPrompt != "stable" {
		t.Fatalf("new request after rollback: %+v", next.Config())
	}
	next.Release()
	held.Release()
	if rem := m.Drain("kb-agent", time.Second); rem != 0 {
		t.Fatalf("drain=%d", rem)
	}
}

func TestPromoteCanaryLeavesOldRequestOnStableConfig(t *testing.T) {
	m := NewManager()
	m.Register("kb-agent", Config{Name: "kb-agent", SystemPrompt: "stable"})
	if _, err := m.StartCanary("kb-agent", Config{SystemPrompt: "canary"}, 10); err != nil {
		t.Fatal(err)
	}
	held, err := m.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetCanaryPercent("kb-agent", 100); err != nil {
		t.Fatal(err)
	}
	if err := m.PromoteCanary("kb-agent"); err != nil {
		t.Fatal(err)
	}
	if held.Config().SystemPrompt != "stable" || !held.IsStale() {
		t.Fatalf("in-flight stable request changed: %+v stale=%v", held.Config(), held.IsStale())
	}
	now, err := m.Acquire("kb-agent")
	if err != nil || now.Config().SystemPrompt != "canary" || now.Config().Version != 2 {
		t.Fatalf("promoted=%+v err=%v", now.Config(), err)
	}
	now.Release()
	held.Release()
	if rem := m.Drain("kb-agent", time.Second); rem != 0 {
		t.Fatalf("drain=%d", rem)
	}
}
