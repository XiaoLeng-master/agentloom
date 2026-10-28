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
	"strings"
	"time"
)

// RuntimeTrace is the minimal, privacy-conscious execution evidence consumed
// by the first trace-driven evaluation stage. Query and answer text are not
// copied into the persisted batch report.
type RuntimeTrace struct {
	ID               string
	Query            string
	Answer           string
	ContractPhases   []string
	RetrievalSources []string
}

// TraceEvaluation records deterministic checks over one runtime trace.
type TraceEvaluation struct {
	TraceID  string   `json:"trace_id"`
	Passed   bool     `json:"passed"`
	Checks   []string `json:"checks"`
	Failures []string `json:"failures,omitempty"`
}

// TraceBatch links real request traces to a reproducible evaluation result.
// The runtime_evidence_rules mode checks execution completeness and retrieval
// evidence; it does not claim semantic answer correctness.
type TraceBatch struct {
	ID              string            `json:"id"`
	CreatedAt       time.Time         `json:"created_at"`
	Mode            string            `json:"mode"`
	TraceIDs        []string          `json:"trace_ids"`
	Reports         []TraceEvaluation `json:"reports"`
	FailureClusters map[string]int    `json:"failure_clusters"`
	Passed          int               `json:"passed"`
	Failed          int               `json:"failed"`
}

// NewTraceBatch evaluates actual runtime traces without persisting prompt or
// answer content in the batch evidence file.
func NewTraceBatch(traces []RuntimeTrace) (TraceBatch, error) {
	if len(traces) == 0 {
		return TraceBatch{}, fmt.Errorf("empty trace batch")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return TraceBatch{}, err
	}
	batch := TraceBatch{ID: hex.EncodeToString(random[:]), CreatedAt: time.Now().UTC(), Mode: "runtime_evidence_rules", FailureClusters: map[string]int{}}
	for _, trace := range traces {
		report := evaluateRuntimeTrace(trace)
		batch.TraceIDs = append(batch.TraceIDs, trace.ID)
		batch.Reports = append(batch.Reports, report)
		if report.Passed {
			batch.Passed++
		} else {
			batch.Failed++
			for _, failure := range report.Failures {
				batch.FailureClusters[failure]++
			}
		}
	}
	return batch, nil
}

func evaluateRuntimeTrace(trace RuntimeTrace) TraceEvaluation {
	report := TraceEvaluation{TraceID: trace.ID}
	check := func(name string, passed bool) {
		report.Checks = append(report.Checks, name)
		if !passed {
			report.Failures = append(report.Failures, name)
		}
	}
	check("query_present", strings.TrimSpace(trace.Query) != "")
	check("answer_present", strings.TrimSpace(trace.Answer) != "")
	check("retrieval_evidence_present", len(trace.RetrievalSources) > 0)
	want := map[string]bool{"plan": false, "execute": false, "verify": false}
	for _, phase := range trace.ContractPhases {
		if _, ok := want[phase]; ok {
			want[phase] = true
		}
	}
	check("contract_chain_complete", want["plan"] && want["execute"] && want["verify"])
	report.Passed = len(report.Failures) == 0
	return report
}

// TraceBatchHistory stores trace-driven batch evidence as atomic JSON files.
type TraceBatchHistory struct{ Dir string }

func (h TraceBatchHistory) Save(batch TraceBatch) error {
	if !validEvidenceID(batch.ID) {
		return fmt.Errorf("invalid trace batch id")
	}
	if err := os.MkdirAll(h.Dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(batch, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(h.Dir, ".trace-batch-*")
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
	return os.Rename(tmp.Name(), filepath.Join(h.Dir, batch.ID+".json"))
}

var ErrTraceBatchNotFound = errors.New("trace batch not found")

func (h TraceBatchHistory) Get(id string) (TraceBatch, error) {
	if !validEvidenceID(id) {
		return TraceBatch{}, ErrTraceBatchNotFound
	}
	data, err := os.ReadFile(filepath.Join(h.Dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return TraceBatch{}, ErrTraceBatchNotFound
	}
	if err != nil {
		return TraceBatch{}, err
	}
	var batch TraceBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return TraceBatch{}, err
	}
	return batch, nil
}

func (h TraceBatchHistory) List() ([]TraceBatch, error) {
	entries, err := os.ReadDir(h.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return []TraceBatch{}, nil
	}
	if err != nil {
		return nil, err
	}
	var batches []TraceBatch
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		batch, err := h.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	sort.Slice(batches, func(i, j int) bool { return batches[i].CreatedAt.After(batches[j].CreatedAt) })
	return batches, nil
}

func validEvidenceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
