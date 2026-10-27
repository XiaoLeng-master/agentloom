// Package gateway implements the request filter chain that sits in front of
// the Agent runtime: authentication, prompt-injection detection, and
// per-source sliding-window rate limiting. Filters are ordered and pluggable;
// the first denial stops the chain. Admit then waits on ConcurrencyAdmission,
// the single in-flight gate (global slots plus one slot pool per tenant or
// source). The slot is held until the caller releases it; Filter.Allow returns
// too early to do that itself.
package gateway

import "context"

// Request is a gateway-level request envelope.
type Request struct {
	Tenant    string
	Token     string
	Prompt    string
	AgentName string
}

// Filter is one link in the chain. Cheap deterministic filters (auth,
// rate-limit) should run before expensive ones (injection detection) so the
// chain fails fast on the common rejection reasons.
type Filter interface {
	Name() string
	Allow(ctx context.Context, r Request) (ok bool, reason string)
}

// Gateway runs a Filter chain in order; the first denial stops. admission,
// when set, is the in-flight stage after those filters.
type Gateway struct {
	filters   []Filter
	admission *ConcurrencyAdmission
}

// New builds a Gateway with filters in the given order.
func New(filters ...Filter) *Gateway { return &Gateway{filters: filters} }

// Add appends a filter to the end of the chain.
func (g *Gateway) Add(f Filter) { g.filters = append(g.filters, f) }

// SetAdmission installs the in-flight gate used by Admit. Call it before
// serving. Nil disables the stage.
func (g *Gateway) SetAdmission(a *ConcurrencyAdmission) { g.admission = a }

// Admission returns the installed in-flight gate, or nil.
func (g *Gateway) Admission() *ConcurrencyAdmission { return g.admission }

// Allow runs the chain. Returns (allowed, deniedBy, reason).
func (g *Gateway) Allow(ctx context.Context, r Request) (bool, string, string) {
	for _, f := range g.filters {
		ok, reason := f.Allow(ctx, r)
		if !ok {
			return false, f.Name(), reason
		}
	}
	return true, "", ""
}

// Admit runs the filter chain, then waits for a global slot and a per-key
// slot. release is nil when a filter denies the request or the wait ends
// with the context. deniedBy is "concurrency" for a queue timeout. The
// caller must release after the handler finishes.
func (g *Gateway) Admit(ctx context.Context, r Request) (release func(), deniedBy, reason string) {
	ok, by, why := g.Allow(ctx, r)
	if !ok {
		return nil, by, why
	}
	if g.admission == nil {
		return func() {}, "", ""
	}
	rel, err := g.admission.Enter(ctx, r)
	if err != nil {
		return nil, "concurrency", err.Error()
	}
	return rel, "", ""
}
