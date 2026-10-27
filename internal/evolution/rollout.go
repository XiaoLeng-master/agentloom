package evolution

import (
	"fmt"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/config"
)

const (
	RolloutCanary   = "canary"
	RolloutRamp     = "ramp"
	RolloutRollback = "rollback"
)

// RolloutEvent is one canary, ramp, or rollback record.
type RolloutEvent struct {
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	Kind          string    `json:"kind"`
	Agent         string    `json:"agent"`
	Percent       int       `json:"percent"`
	StableVersion int       `json:"stable_version"`
	CanaryVersion int       `json:"canary_version"`
	Reason        string    `json:"reason"`
	Stable        Result    `json:"stable"`
	Canary        Result    `json:"canary"`
}

// Rollout drives a gate-passed candidate through a canary window.
// It does not replace the stable config until the last ramp reaches 100%.
type Rollout struct {
	Manager       *config.Manager
	Name          string
	Steps         []int
	History       *History
	index         int
	stableVersion int
	canaryVersion int
}

// NewRollout uses 10%, 50%, then 100% when Steps is empty.
func NewRollout(manager *config.Manager, name string) *Rollout {
	return &Rollout{Manager: manager, Name: name, Steps: []int{10, 50, 100}}
}

// Start puts the candidate into the first traffic step.
func (r *Rollout) Start(candidate config.Config) (RolloutEvent, error) {
	if len(r.Steps) == 0 {
		r.Steps = []int{10, 50, 100}
	}
	version, err := r.Manager.StartCanary(r.Name, candidate, r.Steps[0])
	if err != nil {
		return RolloutEvent{}, err
	}
	r.index = 0
	r.stableVersion = version - 1
	r.canaryVersion = version
	event := RolloutEvent{
		Kind:          RolloutCanary,
		Agent:         r.Name,
		Percent:       r.Steps[0],
		StableVersion: r.stableVersion,
		CanaryVersion: r.canaryVersion,
		Reason:        "gate passed; canary started without replacing the stable config",
	}
	saved, err := r.persist(event)
	if err != nil {
		_ = r.Manager.RollbackCanary(r.Name)
		r.stableVersion = 0
		r.canaryVersion = 0
		return RolloutEvent{}, err
	}
	return saved, nil
}

// Observe compares one window and either ramps, finishes at 100%, or rolls back.
// The samples are whatever the caller measured. This function does not invent them.
func (r *Rollout) Observe(stable, canary Result) (RolloutEvent, error) {
	percent, ok := r.Manager.CanaryPercent(r.Name)
	if !ok {
		return RolloutEvent{}, config.ErrNoCanary
	}
	if stable.Cases <= 0 || canary.Cases <= 0 {
		return RolloutEvent{}, fmt.Errorf("observation window has no samples")
	}
	if regressed(stable, canary) {
		if err := r.Manager.RollbackCanary(r.Name); err != nil {
			return RolloutEvent{}, err
		}
		return r.persist(RolloutEvent{
			Kind: RolloutRollback, Agent: r.Name, Percent: 0,
			StableVersion: r.stableVersion, CanaryVersion: r.canaryVersion,
			Reason: rollbackReason(stable, canary), Stable: stable, Canary: canary,
		})
	}
	next := r.nextPercent(percent)
	if next == percent {
		return RolloutEvent{}, fmt.Errorf("canary is already at the last step")
	}
	if err := r.Manager.SetCanaryPercent(r.Name, next); err != nil {
		return RolloutEvent{}, err
	}
	reason := fmt.Sprintf("observation stable; traffic moved from %d%% to %d%%", percent, next)
	if next == 100 {
		if err := r.Manager.PromoteCanary(r.Name); err != nil {
			return RolloutEvent{}, err
		}
		reason = "observation stable; ramped to 100% and the candidate is now the active config"
	}
	return r.persist(RolloutEvent{
		Kind: RolloutRamp, Agent: r.Name, Percent: next,
		StableVersion: r.stableVersion, CanaryVersion: r.canaryVersion,
		Reason: reason, Stable: stable, Canary: canary,
	})
}

func (r *Rollout) nextPercent(current int) int {
	for i, step := range r.Steps {
		if step == current && i+1 < len(r.Steps) {
			r.index = i + 1
			return r.Steps[i+1]
		}
	}
	for _, step := range r.Steps {
		if step > current {
			return step
		}
	}
	return current
}

func (r *Rollout) persist(event RolloutEvent) (RolloutEvent, error) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.ID == "" {
		id, err := newExperimentID()
		if err != nil {
			return RolloutEvent{}, err
		}
		event.ID = id
	}
	if r.History != nil {
		if err := r.History.SaveRollout(event); err != nil {
			return RolloutEvent{}, err
		}
	}
	return event, nil
}

func regressed(stable, canary Result) bool {
	// 持平不算回落。比较的是调用方给出的窗口，不补样本。
	return canary.HitRate < stable.HitRate ||
		canary.Quality < stable.Quality ||
		canary.NewUnsafeFailures > 0 ||
		canary.FailureCount > stable.FailureCount ||
		canary.GroundingFailures > stable.GroundingFailures
}

func rollbackReason(stable, canary Result) string {
	switch {
	case canary.NewUnsafeFailures > 0:
		return "canary introduced unsafe failures"
	case canary.HitRate < stable.HitRate:
		return fmt.Sprintf("canary hit rate %.4f fell below stable %.4f", canary.HitRate, stable.HitRate)
	case canary.Quality < stable.Quality:
		return fmt.Sprintf("canary quality %.4f fell below stable %.4f", canary.Quality, stable.Quality)
	case canary.FailureCount > stable.FailureCount:
		return fmt.Sprintf("canary failures %d rose above stable %d", canary.FailureCount, stable.FailureCount)
	default:
		return fmt.Sprintf("canary grounding failures %d rose above stable %d", canary.GroundingFailures, stable.GroundingFailures)
	}
}
