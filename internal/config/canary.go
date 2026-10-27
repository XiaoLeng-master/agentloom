package config

import (
	"fmt"
	"hash/fnv"
	"sync/atomic"
)

// ErrCanaryActive is returned when a canary is already running for the agent.
var ErrCanaryActive = fmt.Errorf("config: canary already active")

// ErrNoCanary is returned when a ramp or rollback is requested without one.
var ErrNoCanary = fmt.Errorf("config: no canary")

// ErrBadPercent is returned when a traffic percent is outside 1..100.
var ErrBadPercent = fmt.Errorf("config: canary percent out of range")

// canarySlot is the candidate version served to a slice of tenants.
// The stable version stays in Manager.active until PromoteCanary.
type canarySlot struct {
	entry   *entry
	percent int
}

// TenantBucket maps a tenant onto a stable 0..99 bucket.
func TenantBucket(tenant string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(tenant))
	return int(h.Sum32() % 100)
}

// InCanary reports whether tenant falls in the canary slice.
// Percent is the number of buckets, so 10 means buckets 0..9.
func InCanary(tenant string, percent int) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	return TenantBucket(tenant) < percent
}

// TenantForBucket returns one tenant whose bucket is exactly bucket.
func TenantForBucket(bucket int) (string, bool) {
	if bucket < 0 || bucket > 99 {
		return "", false
	}
	for i := 0; i < 100000; i++ {
		id := fmt.Sprintf("tenant-%d", i)
		if TenantBucket(id) == bucket {
			return id, true
		}
	}
	return "", false
}

// StartCanary installs candidate beside the stable config.
// New tenant-routed acquires split by percent. Acquire without a tenant
// keeps returning the stable version. In-flight handles are not modified.
func (m *Manager) StartCanary(name string, candidate Config, percent int) (int, error) {
	if percent < 1 || percent > 100 {
		return 0, ErrBadPercent
	}
	if candidate.SystemPrompt == "" {
		return 0, fmt.Errorf("config: canary requires a system prompt")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.canaries == nil {
		m.canaries = map[string]*canarySlot{}
	}
	if _, ok := m.canaries[name]; ok {
		return 0, ErrCanaryActive
	}
	stable, ok := m.active[name]
	if !ok {
		return 0, ErrUnknownAgent
	}
	candidate.Name = name
	candidate.Version = stable.cfg.Version + 1
	m.canaries[name] = &canarySlot{entry: &entry{cfg: candidate}, percent: percent}
	return candidate.Version, nil
}

// CanaryConfig returns a copy of the candidate and its percent.
// The second result is false when the agent has no canary.
func (m *Manager) CanaryConfig(name string) (Config, int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	slot, ok := m.canaries[name]
	if !ok {
		return Config{}, 0, false
	}
	return slot.entry.cfg, slot.percent, true
}

// CanaryPercent reports the current canary share. The second result is false
// when the agent has no canary.
func (m *Manager) CanaryPercent(name string) (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	slot, ok := m.canaries[name]
	if !ok {
		return 0, false
	}
	return slot.percent, true
}

// SetCanaryPercent changes the share of an existing canary.
// Widening the percent only adds buckets; a tenant already in the canary stays there.
func (m *Manager) SetCanaryPercent(name string, percent int) error {
	if percent < 1 || percent > 100 {
		return ErrBadPercent
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	slot, ok := m.canaries[name]
	if !ok {
		return ErrNoCanary
	}
	slot.percent = percent
	return nil
}

// RollbackCanary stops routing new requests to the candidate.
// Handles that already acquired the candidate keep that config until Release.
func (m *Manager) RollbackCanary(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	slot, ok := m.canaries[name]
	if !ok {
		return ErrNoCanary
	}
	delete(m.canaries, name)
	slot.entry.stale.Store(true)
	m.drained[name] = append(m.drained[name], slot.entry)
	return nil
}

// PromoteCanary makes the candidate the only active version.
// Stable handles already in flight keep the old config and are drained.
func (m *Manager) PromoteCanary(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	slot, ok := m.canaries[name]
	if !ok {
		return ErrNoCanary
	}
	delete(m.canaries, name)
	if old, ok := m.active[name]; ok {
		old.stale.Store(true)
		m.drained[name] = append(m.drained[name], old)
	}
	m.active[name] = slot.entry
	return nil
}

// AcquireTenant routes a new request to the stable or canary version.
// The same tenant always lands in the same bucket for a given percent.
func (m *Manager) AcquireTenant(name, tenant string) (*AgentHandle, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	slot := m.canaries[name]
	target := m.active[name]
	if target == nil && slot == nil {
		return nil, ErrUnknownAgent
	}
	if slot != nil && InCanary(tenant, slot.percent) {
		target = slot.entry
	}
	if target == nil {
		return nil, ErrUnknownAgent
	}
	return acquireEntry(target), nil
}

func acquireEntry(e *entry) *AgentHandle {
	atomic.AddInt32(&e.refs, 1)
	return &AgentHandle{cfg: e.cfg, refs: &e.refs, stale: &e.stale}
}
