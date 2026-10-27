package gateway

import (
	"context"

	"github.com/XiaoLeng-master/agentloom/internal/quota"
)

// ConcurrencyAdmission is a two-level concurrency quota for requests sitting
// in front of the runtime. RateLimitFilter still rejects immediately when a
// sliding window is full; Enter instead queues until the global semaphore and
// the per-key semaphore both have room, or until ctx ends. A timed-out or
// cancelled wait is counted as a rejection.
//
// release must be called when the admitted request finishes. The key is the
// tenant (for /v1/chat, the request source address) when set, otherwise the
// agent name.
type ConcurrencyAdmission struct {
	lim *quota.Limiter
}

// NewConcurrencyAdmission returns an admission gate. Non-positive limits are
// clamped to 1 by the underlying limiter.
func NewConcurrencyAdmission(global, perKey int) *ConcurrencyAdmission {
	return &ConcurrencyAdmission{lim: quota.New(global, perKey)}
}

// Enter waits for a global slot and a per-tenant (or per-agent) slot.
func (a *ConcurrencyAdmission) Enter(ctx context.Context, r Request) (func(), error) {
	key := r.Tenant
	if key == "" {
		key = r.AgentName
	}
	return a.lim.Acquire(ctx, key)
}

// Stats returns concurrency, queue depth, and rejection counters.
func (a *ConcurrencyAdmission) Stats() quota.Stats {
	return a.lim.Stats()
}
