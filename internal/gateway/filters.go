package gateway

import (
	"context"
	"strings"
	"sync"
	"time"
)

// AuthFilter rejects requests without a non-empty token. In production this
// is replaced by JWT/OAuth2/LDAP; the seam is the same.
type AuthFilter struct{}

func (AuthFilter) Name() string { return "auth" }
func (AuthFilter) Allow(_ context.Context, r Request) (bool, string) {
	if strings.TrimSpace(r.Token) == "" {
		return false, "missing token"
	}
	return true, ""
}

// InjectionFilter detects obvious prompt-injection markers in the user prompt:
// attempts to override prior instructions or impersonate the system.
type InjectionFilter struct{}

func (InjectionFilter) Name() string { return "injection" }
func (InjectionFilter) Allow(_ context.Context, r Request) (bool, string) {
	low := strings.ToLower(r.Prompt)
	for _, mark := range []string{"ignore previous", "ignore all previous", "system:", "you are now", "disregard the above"} {
		if strings.Contains(low, mark) {
			return false, "prompt injection marker: " + mark
		}
	}
	return true, ""
}

// RateLimitFilter is a per-tenant sliding-window limiter (requests per window).
// Production uses Redis so the count is shared across instances; the local
// mutex version is for tests and the single-node demo.
type RateLimitFilter struct {
	PerWindow int
	Window    time.Duration
	mu        sync.Mutex
	hits      map[string][]time.Time
}

func NewRateLimitFilter(perWindow int, window time.Duration) *RateLimitFilter {
	return &RateLimitFilter{PerWindow: perWindow, Window: window, hits: map[string][]time.Time{}}
}

func (f *RateLimitFilter) Name() string { return "rate-limit" }
func (f *RateLimitFilter) Allow(_ context.Context, r Request) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-f.Window)
	keep := f.hits[r.Tenant][:0]
	for _, t := range f.hits[r.Tenant] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	f.hits[r.Tenant] = keep
	if len(keep) >= f.PerWindow {
		return false, "rate limit exceeded for tenant"
	}
	f.hits[r.Tenant] = append(f.hits[r.Tenant], now)
	return true, ""
}
