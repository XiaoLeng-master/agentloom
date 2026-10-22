package eval

// Score is one evaluator's verdict on a trace.
type Score struct {
	Name   string
	Value  float32 // 0..1
	Reason string
	Pass   bool
}

// Evaluator scores one dimension of a Trace. Implementations are registered
// in a Registry; the harness runs all of them per trace and aggregates.
type Evaluator interface {
	Name() string
	Eval(t Trace) Score
}

// Registry holds evaluators. Add rules-first, LLM-judge last so the cheap
// deterministic checks gate the expensive model calls.
type Registry struct{ evals []Evaluator }

// NewRegistry returns a Registry pre-loaded with evaluators.
func NewRegistry(es ...Evaluator) *Registry { return &Registry{evals: es} }

// Add appends an evaluator.
func (r *Registry) Add(e Evaluator) { r.evals = append(r.evals, e) }

// All returns the registered evaluators.
func (r *Registry) All() []Evaluator { return r.evals }

// Eval runs every evaluator and returns their scores.
func (r *Registry) Eval(t Trace) []Score {
	out := make([]Score, 0, len(r.evals))
	for _, e := range r.evals {
		out = append(out, e.Eval(t))
	}
	return out
}
