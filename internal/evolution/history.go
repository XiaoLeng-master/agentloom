package evolution

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Experiment records a single retrieval comparison and its gate decision.
type Experiment struct {
	ID                 string        `json:"id"`
	CreatedAt          time.Time     `json:"created_at"`
	Before             RetrievalRun  `json:"before"`
	After              RetrievalRun  `json:"after"`
	Failures           []Failure     `json:"failures"`
	Prediction         Prediction    `json:"prediction"`
	ActualDelta        float64       `json:"actual_delta"`
	Decision           Decision      `json:"decision"`
	AnswerQuality      string        `json:"answer_quality"`
	Tasks              []TaskRecord  `json:"tasks,omitempty"`
	CauseCounts        []CauseCount  `json:"cause_counts,omitempty"`
	Promoted           *Promotion    `json:"promoted,omitempty"`
	Rollout            *RolloutEvent `json:"rollout,omitempty"`
	ProxyMode          string        `json:"proxy_mode,omitempty"`
	QualityBefore      float64       `json:"quality_before,omitempty"`
	QualityAfter       float64       `json:"quality_after,omitempty"`
	HitRateDelta       float64       `json:"hit_rate_delta,omitempty"`
	FailureCountBefore int           `json:"failure_count_before,omitempty"`
	FailureCountAfter  int           `json:"failure_count_after,omitempty"`
	GroundingBefore    int           `json:"grounding_failures_before,omitempty"`
	GroundingAfter     int           `json:"grounding_failures_after,omitempty"`
}

// NewExperiment gives each run a random identity and explicit quality boundary.
func newExperimentID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func NewExperiment(before, after RetrievalRun, prediction Prediction, decision Decision) (Experiment, error) {
	id, err := newExperimentID()
	if err != nil {
		return Experiment{}, err
	}
	quality := "not_evaluated"
	if after.QualityMode != "" {
		quality = after.QualityMode
	} else if before.QualityMode != "" {
		quality = before.QualityMode
	}
	return Experiment{ID: id, CreatedAt: time.Now().UTC(), Before: before, After: after,
		Failures: DiagnoseRetrieval(before, after), Prediction: prediction, ActualDelta: after.HitRate - before.HitRate,
		Decision: decision, AnswerQuality: quality}, nil
}

// NewLoopExperiment stores the task-driven retest. Answer quality is the
// measured deterministic proxy, not a stand-in for an LLM judge.
func NewLoopExperiment(report LoopReport) (Experiment, error) {
	id, err := newExperimentID()
	if err != nil {
		return Experiment{}, err
	}
	beforeMetrics := report.Baseline.Metrics
	before := retrievalFromMetrics(beforeMetrics, report.DatasetHash, report.Baseline)
	after := before
	afterMetrics := beforeMetrics
	prediction := Prediction{}
	decision := Decision{Passed: false, Reason: "no candidate passed the gate"}
	if report.Promoted != nil {
		for _, candidate := range report.Candidates {
			if candidate.Task.ID != report.Promoted.TaskID {
				continue
			}
			afterMetrics = candidate.Metrics
			after = retrievalFromMetrics(afterMetrics, report.DatasetHash, BatchReport{Metrics: candidate.Metrics, Results: candidate.Results})
			prediction = candidate.Task.Prediction
			decision = candidate.Gate
			break
		}
	}
	tasks := report.Tasks
	if tasks == nil {
		tasks = []TaskRecord{}
	}
	failures := DiagnoseRetrieval(before, after)
	if failures == nil {
		failures = []Failure{}
	}
	return Experiment{
		ID: id, CreatedAt: time.Now().UTC(), Before: before, After: after,
		Failures: failures, Prediction: prediction, ActualDelta: after.HitRate - before.HitRate,
		Decision: decision, AnswerQuality: "deterministic_proxy",
		Tasks: tasks, CauseCounts: report.CauseCounts, Promoted: report.Promoted,
		Rollout:            report.Rollout,
		ProxyMode:          "deterministic_proxy",
		QualityBefore:      beforeMetrics.Quality,
		QualityAfter:       afterMetrics.Quality,
		HitRateDelta:       after.HitRate - before.HitRate,
		FailureCountBefore: beforeMetrics.FailureCount,
		FailureCountAfter:  afterMetrics.FailureCount,
		GroundingBefore:    beforeMetrics.GroundingFailures,
		GroundingAfter:     afterMetrics.GroundingFailures,
	}, nil
}

func retrievalFromMetrics(metrics BatchMetrics, datasetHash string, report BatchReport) RetrievalRun {
	run := RetrievalRun{
		Strategy: metrics.Strategy, DatasetHash: datasetHash, Cases: metrics.Cases, HitRate: metrics.HitRate,
		QualityMode: "deterministic_proxy", Quality: metrics.Quality, GroundingFailures: metrics.GroundingFailures,
		PromptMode: metrics.PromptMode, TopK: metrics.TopK,
	}
	for _, result := range report.Results {
		run.Results = append(run.Results, CaseResult{
			ID: result.ID, ExpectedSource: result.ExpectedSource, RetrievedSources: result.RetrievedSources, Hit: result.Hit,
		})
	}
	return run
}

var ErrExperimentNotFound = errors.New("experiment not found")

// History stores public-dataset experiments as separate JSON files.
type History struct{ Dir string }

// Save persists one experiment atomically; IDs are generated by NewExperiment.
func (h History) Save(exp Experiment) error {
	if len(exp.ID) != 32 {
		return fmt.Errorf("invalid experiment id")
	}
	return writeJSONFile(h.Dir, exp.ID+".json", exp)
}

// SaveRollout writes a canary, ramp, or rollback event under Dir/rollouts.
// The subdirectory keeps List from treating rollout files as experiments.
func (h History) SaveRollout(event RolloutEvent) error {
	if len(event.ID) != 32 {
		return fmt.Errorf("invalid rollout id")
	}
	return writeJSONFile(filepath.Join(h.Dir, "rollouts"), event.ID+".json", event)
}

// ListRollouts returns canary, ramp, and rollback events newest first.
func (h History) ListRollouts() ([]RolloutEvent, error) {
	dir := filepath.Join(h.Dir, "rollouts")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []RolloutEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RolloutEvent
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var event RolloutEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, err
		}
		if event.ID != id {
			return nil, fmt.Errorf("rollout id mismatch")
		}
		out = append(out, event)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func writeJSONFile(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// Get reads a saved experiment by ID.
func (h History) Get(id string) (Experiment, error) {
	if len(id) != 32 {
		return Experiment{}, ErrExperimentNotFound
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return Experiment{}, ErrExperimentNotFound
		}
	}
	data, err := os.ReadFile(filepath.Join(h.Dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Experiment{}, ErrExperimentNotFound
	}
	if err != nil {
		return Experiment{}, err
	}
	var exp Experiment
	if err := json.Unmarshal(data, &exp); err != nil {
		return Experiment{}, err
	}
	return exp, nil
}

// List returns saved experiments newest first.
func (h History) List() ([]Experiment, error) {
	entries, err := os.ReadDir(h.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Experiment{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Experiment
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		exp, err := h.Get(entry.Name()[:len(entry.Name())-5])
		if err != nil {
			return nil, err
		}
		out = append(out, exp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
