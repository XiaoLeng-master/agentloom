package evolution

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/config"
)

func TestRolloutRampPromotesAt100(t *testing.T) {
	dir := t.TempDir()
	history := &History{Dir: dir}
	rollout, mgr := newScriptedRollout(t, history)
	started := startScriptedCanary(t, rollout)
	if started.Kind != RolloutCanary || started.Percent != 10 || started.StableVersion != 1 || started.CanaryVersion != 2 || started.StableVersion == started.Canary.Cases {
		t.Fatalf("start=%+v", started)
	}
	stable := Result{Cases: 20, HitRate: 0.40, Quality: 0.50, FailureCount: 8, GroundingFailures: 3}
	canary := Result{Cases: 20, HitRate: 0.40, Quality: 0.50, FailureCount: 8, GroundingFailures: 3}
	ramp, err := rollout.Observe(stable, canary)
	if err != nil || ramp.Kind != RolloutRamp || ramp.Percent != 50 || ramp.StableVersion != 1 || ramp.CanaryVersion != 2 {
		t.Fatalf("ramp=%+v err=%v", ramp, err)
	}
	canary.HitRate = 0.46
	canary.Quality = 0.55
	done, err := rollout.Observe(stable, canary)
	if err != nil || done.Kind != RolloutRamp || done.Percent != 100 || done.StableVersion != 1 || done.CanaryVersion != 2 {
		t.Fatalf("done=%+v err=%v", done, err)
	}
	if _, ok := mgr.CanaryPercent("kb-agent"); ok {
		t.Fatal("canary slot remained after 100%")
	}
	active, err := mgr.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Release()
	cfg := active.Config()
	if cfg.Version != 2 || cfg.ChunkStrategy != "recursive" || cfg.TopK != 5 || cfg.SystemPrompt != "canary" {
		t.Fatalf("active=%+v", cfg)
	}
	experiments, err := history.List()
	if err != nil || len(experiments) != 0 {
		t.Fatalf("experiments=%d err=%v", len(experiments), err)
	}
	events, err := history.ListRollouts()
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestRolloutRegressionRollsBackWithVersions(t *testing.T) {
	stable := Result{Cases: 20, HitRate: 0.40, Quality: 0.50, FailureCount: 8, GroundingFailures: 3}
	cases := []struct {
		name   string
		canary Result
	}{
		{name: "hit drop", canary: Result{Cases: 20, HitRate: 0.30, Quality: 0.55, FailureCount: 8, GroundingFailures: 3}},
		{name: "quality drop", canary: Result{Cases: 20, HitRate: 0.46, Quality: 0.40, FailureCount: 8, GroundingFailures: 3}},
		{name: "unsafe", canary: Result{Cases: 20, HitRate: 0.46, Quality: 0.55, NewUnsafeFailures: 1, FailureCount: 8, GroundingFailures: 3}},
		{name: "more failures", canary: Result{Cases: 20, HitRate: 0.46, Quality: 0.55, FailureCount: 9, GroundingFailures: 3}},
		{name: "more grounding failures", canary: Result{Cases: 20, HitRate: 0.46, Quality: 0.55, FailureCount: 8, GroundingFailures: 4}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rollout, mgr := newScriptedRollout(t, nil)
			started := startScriptedCanary(t, rollout)
			event, err := rollout.Observe(stable, tc.canary)
			if err != nil || event.Kind != RolloutRollback || event.Percent != 0 {
				t.Fatalf("event=%+v err=%v", event, err)
			}
			if event.StableVersion != 1 || event.CanaryVersion != 2 || event.StableVersion == stable.Cases || event.CanaryVersion == tc.canary.Cases {
				t.Fatalf("versions stable=%d canary=%d start=%+v", event.StableVersion, event.CanaryVersion, started)
			}
			if _, ok := mgr.CanaryPercent("kb-agent"); ok {
				t.Fatal("canary remained after rollback")
			}
			active, err := mgr.Acquire("kb-agent")
			if err != nil {
				t.Fatal(err)
			}
			defer active.Release()
			if active.Config().Version != 1 || active.Config().SystemPrompt != "stable" {
				t.Fatalf("active=%+v", active.Config())
			}
		})
	}
}

func TestRolloutRollbackKeepsInFlight(t *testing.T) {
	rollout, mgr := newScriptedRollout(t, nil)
	startScriptedCanary(t, rollout)
	tenant, ok := config.TenantForBucket(0)
	if !ok {
		t.Fatal("missing canary tenant")
	}
	held, err := mgr.AcquireTenant("kb-agent", tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	before := held.Config()
	event, err := rollout.Observe(
		Result{Cases: 20, HitRate: 0.40, Quality: 0.50},
		Result{Cases: 20, HitRate: 0.30, Quality: 0.40},
	)
	if err != nil || event.Kind != RolloutRollback {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	if held.Config().Version != before.Version || held.Config().SystemPrompt != before.SystemPrompt || held.Config().ChunkStrategy != before.ChunkStrategy || held.Config().TopK != before.TopK || held.Config().Version != 2 || held.Config().SystemPrompt != "canary" {
		t.Fatalf("in-flight changed: before=%+v now=%+v", before, held.Config())
	}
	if mgr.Drain("kb-agent", 20*time.Millisecond) == 0 {
		t.Fatal("drain finished while the canary handle was still held")
	}
	next, err := mgr.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	if next.Config().Version != 1 || next.Config().SystemPrompt != "stable" {
		t.Fatalf("new acquire=%+v", next.Config())
	}
}

func TestRolloutEmptyWindowKeepsCanary(t *testing.T) {
	rollout, mgr := newScriptedRollout(t, nil)
	startScriptedCanary(t, rollout)
	_, err := rollout.Observe(Result{}, Result{Cases: 20, HitRate: 0.5, Quality: 0.5})
	if err == nil {
		t.Fatal("empty window was accepted")
	}
	percent, ok := mgr.CanaryPercent("kb-agent")
	if !ok || percent != 10 {
		t.Fatalf("percent=%d ok=%v", percent, ok)
	}
}

func TestRolloutPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	rollout, mgr := newScriptedRollout(t, &History{Dir: blocker})
	_, err := rollout.Start(config.Config{Name: "kb-agent", SystemPrompt: "canary", ChunkStrategy: "recursive", TopK: 5})
	if err == nil {
		t.Fatal("persist failure was ignored")
	}
	if _, ok := mgr.CanaryPercent("kb-agent"); ok {
		t.Fatal("canary stayed active after the rollout record failed")
	}
	active, err := mgr.Acquire("kb-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Release()
	if active.Config().Version != 1 || active.Config().SystemPrompt != "stable" {
		t.Fatalf("active=%+v", active.Config())
	}
}

func newScriptedRollout(t *testing.T, history *History) (*Rollout, *config.Manager) {
	t.Helper()
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "stable", ChunkStrategy: "fixed", TopK: 3})
	rollout := NewRollout(mgr, "kb-agent")
	rollout.History = history
	return rollout, mgr
}

func startScriptedCanary(t *testing.T, rollout *Rollout) RolloutEvent {
	t.Helper()
	event, err := rollout.Start(config.Config{Name: "kb-agent", SystemPrompt: "canary", ChunkStrategy: "recursive", TopK: 5})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
