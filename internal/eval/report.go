package eval

import (
	"fmt"
	"strings"
)

// Report aggregates evaluator scores for one trace. Pass is true only if every
// evaluator passed (a single critical failure gates the report red).
type Report struct {
	TraceID string
	Scores  []Score
	Pass    bool
}

// Build runs the registry over a trace and aggregates.
func Build(t Trace, r *Registry) Report {
	scores := r.Eval(t)
	pass := true
	for _, s := range scores {
		if !s.Pass {
			pass = false
			break
		}
	}
	return Report{TraceID: t.ID, Scores: scores, Pass: pass}
}

// Markdown renders a one-trace report (the harness concatenates these).
func (rp Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s\n\n", rp.TraceID, status(rp.Pass))
	b.WriteString("| 维度 | 得分 | 通过 | 原因 |\n|---|---|---|---|\n")
	for _, s := range rp.Scores {
		fmt.Fprintf(&b, "| %s | %.2f | %s | %s |\n", s.Name, s.Value, status(s.Pass), s.Reason)
	}
	b.WriteString("\n")
	return b.String()
}

func status(p bool) string {
	if p {
		return "✅"
	}
	return "❌"
}
