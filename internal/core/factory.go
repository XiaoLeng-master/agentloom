package core

import (
	"fmt"
	"sync"
)

// AgentConfig captures the inputs needed to build an Agent without binding it
// to a specific LLMClient; the factory closes over the client.
type AgentConfig struct {
	Name         string
	SystemPrompt string
	Tools        []Tool
	MaxIter      int
}

// AgentFactory builds an Agent from a config. It is registered once per agent
// type and reused across config changes; this is the seam where D2 will plug
// in hot-reload (a config change replaces the factory, and in-flight agents
// are drained via reference counting before swap).
type AgentFactory func(cfg AgentConfig) (*Agent, error)

// Registry maps agent names to factories. It is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	factories map[string]AgentFactory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]AgentFactory)}
}

// Register associates name with f. A later registration with the same name
// replaces the previous one — this is intentional, it is how D2 hot-reload
// will swap a factory without restarting the process.
func (r *Registry) Register(name string, f AgentFactory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[name] = f
}

// Create builds the named agent. It returns an error if name is unknown.
func (r *Registry) Create(name string, cfg AgentConfig) (*Agent, error) {
	r.mu.RLock()
	f, ok := r.factories[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: unknown agent %q", name)
	}
	return f(cfg)
}

// Names returns a snapshot of registered agent names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.factories))
	for n := range r.factories {
		out = append(out, n)
	}
	return out
}
