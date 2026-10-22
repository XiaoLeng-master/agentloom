// Package gateway implements the request filter chain that sits in front of
// the Agent runtime: authentication, prompt-injection detection, and
// per-tenant rate limiting. Filters are ordered and pluggable; the first
// denial stops the chain. This is the "total control plane" that multi-agent
// collaboration and the eval harness gray-rollout both run through.
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

// Gateway runs a Filter chain in order; the first denial stops.
type Gateway struct{ filters []Filter }

// New builds a Gateway with filters in the given order.
func New(filters ...Filter) *Gateway { return &Gateway{filters: filters} }

// Add appends a filter to the end of the chain.
func (g *Gateway) Add(f Filter) { g.filters = append(g.filters, f) }

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
