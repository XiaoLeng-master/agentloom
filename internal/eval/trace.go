// Package eval implements the evaluation harness for agent traces: a Trace
// records one agent turn, a Registry of Evaluators scores it across
// dimensions (prompt / rag / skill / agent / g-eval), and a Report aggregates
// the scores. This is the "see what failed and why" half of the self-evolving
// loop; the other half (D2 hot-reload) ships the fix.
package eval

import "time"

// EventKind labels a trace step.
type EventKind string

const (
	EventLLM       EventKind = "llm"
	EventTool      EventKind = "tool"
	EventRetrieve  EventKind = "retrieve"
)

// Event is one step in an agent run.
type Event struct {
	Kind    EventKind
	Name    string
	Input   string
	Output  string
	Latency time.Duration
	Tokens  int
}

// Expected is the ground truth for a case.
type Expected struct {
	Answer     string
	Tools      []string
	GroundedIn []string // source docs the answer should cite
}

// Trace is the structured record of one agent turn, consumed by evaluators.
type Trace struct {
	ID        string
	Query     string
	Answer    string
	Citations []string
	Events    []Event
	Tools     []string
	Expected  Expected
}

// Latency returns total wall time across events.
func (t Trace) Latency() time.Duration {
	var d time.Duration
	for _, e := range t.Events {
		d += e.Latency
	}
	return d
}
