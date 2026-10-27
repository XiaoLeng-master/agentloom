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
