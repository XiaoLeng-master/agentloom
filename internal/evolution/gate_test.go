package evolution

import (
	"errors"
	"testing"

	"github.com/XiaoLeng-master/agentloom/internal/config"
)

func TestPromoteRejectsRegressionAndKeepsActiveVersion(t *testing.T) {
	mgr := config.NewManager()
	mgr.Register("demo", config.Config{Name: "demo", SystemPrompt: "old"})
	_, err := Promote(mgr, "demo", config.Config{Name: "demo", SystemPrompt: "new"}, Result{Cases: 98, HitRate: 0.4, Quality: 0.8}, Result{Cases: 98, HitRate: 0.3, Quality: 0.8}, Prediction{})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err=%v", err)
	}
	h, _ := mgr.Acquire("demo")
	defer h.Release()
	if h.Config().Version != 1 {
		t.Fatalf("version=%d", h.Config().Version)
	}
}

func TestPromoteRecordsPredictionAndKeepsInFlightConfig(t *testing.T) {
	mgr := config.NewManager()
	mgr.Register("demo", config.Config{Name: "demo", SystemPrompt: "old"})
	old, _ := mgr.Acquire("demo")
	decision, err := Promote(mgr, "demo", config.Config{Name: "demo", SystemPrompt: "new"}, Result{Cases: 98, HitRate: 0.4, Quality: 0.8}, Result{Cases: 98, HitRate: 0.45, Quality: 0.8}, Prediction{ExpectedHitRateDeltaMin: 0.04, ExpectedHitRateDeltaMax: 0.06})
	if err != nil || !decision.Passed || !decision.PredictionMatched || decision.Version != 2 {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if old.Config().Version != 1 || !old.IsStale() {
		t.Fatal("in-flight config changed")
	}
	old.Release()
}

func TestJudgeComparesQualityAndRecordsMissedPrediction(t *testing.T) {
	baseline := Result{Cases: 98, HitRate: 0.40, Quality: 0.50, ToolSuccess: 1}
	next := Result{Cases: 98, HitRate: 0.40, Quality: 0.62, ToolSuccess: 1}
	prediction := Prediction{Metric: "quality", ExpectedQualityDeltaMin: 0.20, ExpectedQualityDeltaMax: 0.30}
	decision, err := Judge(baseline, next, prediction)
	if err != nil || !decision.Passed || decision.PredictionMatched {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	unsafe := next
	unsafe.NewUnsafeFailures = 1
	rejected, err := Judge(baseline, unsafe, prediction)
	if err == nil || rejected.Passed || rejected.Reason != "regression gate failed" {
		t.Fatalf("rejected=%+v err=%v", rejected, err)
	}
}

func TestJudgeUsesProxyToleranceAndFailureCount(t *testing.T) {
	baseline := Result{Cases: 4, HitRate: 0.5, Quality: 0.40, FailureCount: 4, GroundingFailures: 1}
	noise := baseline
	noise.Quality = baseline.Quality - 5e-8
	decision, err := Judge(baseline, noise, Prediction{})
	if err != nil || !decision.Passed {
		t.Fatalf("1e-6 tolerance rejected a last-bit drop: %+v err=%v", decision, err)
	}
	drop := baseline
	drop.Quality = baseline.Quality - 1e-4
	if decision, err := Judge(baseline, drop, Prediction{}); err == nil || decision.Passed {
		t.Fatalf("material proxy drop passed: %+v err=%v", decision, err)
	}
	moreFailures := baseline
	moreFailures.FailureCount = 5
	if decision, err := Judge(baseline, moreFailures, Prediction{}); err == nil || decision.Reason != "regression gate failed" {
		t.Fatalf("failure increase passed: %+v err=%v", decision, err)
	}
	citations := baseline
	citations.NewUnsafeFailures = 1
	if decision, err := Judge(baseline, citations, Prediction{}); err == nil || decision.Passed {
		t.Fatalf("new citation failure passed: %+v err=%v", decision, err)
	}
}

func TestDecideCandidateRejectsUnchangedHardMetrics(t *testing.T) {
	baseline := Result{Cases: 98, HitRate: 0.2041, Quality: 0.31, FailureCount: 98, GroundingFailures: 80}
	same := baseline
	same.ToolSuccess = 1
	decision, err := DecideCandidate(baseline, same, Prediction{Metric: "quality", ExpectedQualityDeltaMin: 0.01, ExpectedQualityDeltaMax: 0.2})
	if err == nil || decision.Passed || decision.Reason != "hard metrics unchanged" {
		t.Fatalf("unchanged hard metrics were promoted: %+v err=%v", decision, err)
	}
	fixed := baseline
	fixed.FailureCount = 97
	decision, err = DecideCandidate(baseline, fixed, Prediction{})
	if err != nil || !decision.Passed {
		t.Fatalf("fewer attribution failures should pass: %+v err=%v", decision, err)
	}
}
