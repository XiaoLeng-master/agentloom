// Package evolution provides an offline release gate for versioned config candidates.
package evolution

import (
	"errors"
	"fmt"

	"github.com/XiaoLeng-master/agentloom/internal/config"
)

var ErrRejected = errors.New("evolution: candidate rejected")

// Result represents metrics from the same fixed case set for one candidate.
type Result struct {
	Cases             int
	HitRate           float64
	Quality           float64
	NewUnsafeFailures int
}

// Prediction records a hypothesis before candidate evaluation.
type Prediction struct {
	ExpectedHitRateDeltaMin float64
	ExpectedHitRateDeltaMax float64
}

// Decision records what was checked before a local config replacement.
type Decision struct {
	Passed            bool
	PredictionMatched bool
	Reason            string
	Version           int
	RolledBack        bool
	RollbackVersion   int
}

// Promote replaces the active config only after all deterministic guards pass.
// It does not generate a candidate, deploy to etcd, or measure online outcomes.
func Promote(manager *config.Manager, name string, candidate config.Config, baseline, next Result, prediction Prediction) (Decision, error) {
	if baseline.Cases <= 0 || next.Cases != baseline.Cases {
		return Decision{Reason: "incomparable case sets"}, ErrRejected
	}
	if candidate.Name != name || candidate.SystemPrompt == "" {
		return Decision{Reason: "invalid candidate"}, ErrRejected
	}
	// Quality is an aggregate float from deterministic evaluators; tolerate
	// harmless last-bit drift while still rejecting material regressions.
	if next.HitRate < baseline.HitRate || next.Quality+1e-6 < baseline.Quality || next.NewUnsafeFailures > 0 {
		return Decision{Reason: "regression gate failed"}, ErrRejected
	}
	delta := next.HitRate - baseline.HitRate
	matched := delta >= prediction.ExpectedHitRateDeltaMin && delta <= prediction.ExpectedHitRateDeltaMax
	handle, err := manager.Acquire(name)
	if err != nil {
		return Decision{}, fmt.Errorf("acquire active: %w", err)
	}
	handle.Release()
	manager.Reload(name, candidate)
	active, err := manager.Acquire(name)
	if err != nil {
		return Decision{}, err
	}
	version := active.Config().Version
	active.Release()
	return Decision{Passed: true, PredictionMatched: matched, Reason: "offline gate passed", Version: version}, nil
}
