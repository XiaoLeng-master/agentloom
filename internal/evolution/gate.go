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
	ToolSuccess       float64
	NewUnsafeFailures int
	FailureCount      int
	GroundingFailures int
}

// Prediction records a hypothesis declared before candidate evaluation.
// Metric selects which delta is checked. An empty Metric keeps the original
// hit-rate comparison so existing experiments stay valid.
type Prediction struct {
	Metric                  string  `json:"metric,omitempty"`
	ExpectedHitRateDeltaMin float64 `json:"ExpectedHitRateDeltaMin"`
	ExpectedHitRateDeltaMax float64 `json:"ExpectedHitRateDeltaMax"`
	ExpectedQualityDeltaMin float64 `json:"ExpectedQualityDeltaMin,omitempty"`
	ExpectedQualityDeltaMax float64 `json:"ExpectedQualityDeltaMax,omitempty"`
	ExpectedDeltaMin        float64 `json:"expected_delta_min,omitempty"`
	ExpectedDeltaMax        float64 `json:"expected_delta_max,omitempty"`
}

// Decision records what was checked before a local config replacement.
type Decision struct {
	Passed            bool
	PredictionMatched bool
	Reason            string
	Version           int
}

// MatchPrediction reports whether the actual delta of the declared metric
// fell inside the interval that was fixed before retest.
func MatchPrediction(baseline, next Result, prediction Prediction) bool {
	delta, min, max := predictedDelta(baseline, next, prediction)
	const eps = 1e-9
	return delta+eps >= min && delta-eps <= max
}

func predictedDelta(baseline, next Result, prediction Prediction) (delta, min, max float64) {
	switch prediction.Metric {
	case "quality":
		return next.Quality - baseline.Quality, prediction.ExpectedQualityDeltaMin, prediction.ExpectedQualityDeltaMax
	case "tool_success":
		return next.ToolSuccess - baseline.ToolSuccess, prediction.ExpectedDeltaMin, prediction.ExpectedDeltaMax
	default:
		return next.HitRate - baseline.HitRate, prediction.ExpectedHitRateDeltaMin, prediction.ExpectedHitRateDeltaMax
	}
}

// Judge compares a candidate with the baseline. It does not reload config.
// PredictionMatched is recorded even when the gate rejects the candidate.
func Judge(baseline, next Result, prediction Prediction) (Decision, error) {
	if baseline.Cases <= 0 || next.Cases != baseline.Cases {
		return Decision{Reason: "incomparable case sets"}, ErrRejected
	}
	matched := MatchPrediction(baseline, next, prediction)
	// Quality is the deterministic_proxy aggregate. 1e-6 tolerates last-bit
	// drift and still rejects a real drop. Failure count and new citation
	// failures are hard checks; a weighted rendering score cannot enter here.
	if next.HitRate < baseline.HitRate || next.Quality+1e-6 < baseline.Quality || next.NewUnsafeFailures > 0 || next.FailureCount > baseline.FailureCount {
		return Decision{Passed: false, PredictionMatched: matched, Reason: "regression gate failed"}, ErrRejected
	}
	return Decision{Passed: true, PredictionMatched: matched, Reason: "offline gate passed"}, nil
}

// DecideCandidate applies Judge and then refuses a candidate whose hard
// metrics did not improve. Equality is not a promotion: shortening a rendered
// answer cannot move hit rate, proxy quality, failure count, or citations.
func DecideCandidate(baseline, next Result, prediction Prediction) (Decision, error) {
	decision, err := Judge(baseline, next, prediction)
	if err != nil {
		return decision, err
	}
	if !hardImproved(baseline, next) {
		decision.Passed = false
		decision.Reason = "hard metrics unchanged"
		return decision, ErrRejected
	}
	return decision, nil
}

func hardImproved(baseline, next Result) bool {
	const eps = 1e-6
	return next.HitRate > baseline.HitRate+eps ||
		next.Quality > baseline.Quality+eps ||
		next.FailureCount < baseline.FailureCount ||
		next.GroundingFailures < baseline.GroundingFailures
}

// Promote replaces the active config only after all deterministic guards pass.
// It does not generate a candidate, deploy to etcd, or measure online outcomes.
func Promote(manager *config.Manager, name string, candidate config.Config, baseline, next Result, prediction Prediction) (Decision, error) {
	decision, err := Judge(baseline, next, prediction)
	if err != nil && decision.Reason == "incomparable case sets" {
		return decision, err
	}
	if candidate.Name != name || candidate.SystemPrompt == "" {
		return Decision{Reason: "invalid candidate"}, ErrRejected
	}
	if err != nil {
		return decision, err
	}
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
	decision.Version = version
	return decision, nil
}
