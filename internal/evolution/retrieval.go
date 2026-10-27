package evolution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

// Case identifies the expected source for one public retrieval question.
type Case struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Question    string `json:"question"`
	GroundTruth string `json:"groundTruth"`
	SourceDoc   string `json:"sourceDoc"`
}

// CaseResult keeps per-question evidence so aggregate scores are auditable.
type CaseResult struct {
	ID               string   `json:"id"`
	ExpectedSource   string   `json:"expected_source"`
	RetrievedSources []string `json:"retrieved_sources"`
	Hit              bool     `json:"hit"`
}

// RetrievalRun is a reproducible run against a fixed dataset and corpus.
type RetrievalRun struct {
	Strategy          string             `json:"strategy"`
	DatasetHash       string             `json:"dataset_hash"`
	Cases             int                `json:"cases"`
	HitRate           float64            `json:"hit_rate"`
	QualityMode       string             `json:"quality_mode,omitempty"`
	Quality           float64            `json:"quality,omitempty"`
	GroundingFailures int                `json:"grounding_failures,omitempty"`
	PassRate          float64            `json:"pass_rate,omitempty"`
	EvaluatorScores   map[string]float64 `json:"evaluator_scores,omitempty"`
	Results           []CaseResult       `json:"results"`
}

// RunRetrieval evaluates the same K=3 source-hit metric as cmd/bench.
func RunRetrieval(cases []Case, docs []rag.Document, datasetHash string, splitter rag.Splitter) RetrievalRun {
	r := &rag.Retriever{Splitter: splitter, Embedder: rag.HashEmbedder{DimV: 512}, Store: &rag.MemoryStore{}}
	r.Index(docs)
	out := RetrievalRun{Strategy: splitter.Name(), DatasetHash: datasetHash, Cases: len(cases)}
	for _, c := range cases {
		result := CaseResult{ID: c.ID, ExpectedSource: c.SourceDoc}
		for _, hit := range r.Retrieve(c.Question, 3) {
			result.RetrievedSources = append(result.RetrievedSources, hit.Source)
			if hit.Source == c.SourceDoc {
				result.Hit = true
			}
		}
		if result.Hit {
			out.HitRate++
		}
		out.Results = append(out.Results, result)
	}
	if len(cases) > 0 {
		out.HitRate /= float64(len(cases))
	}
	return out
}

// LoadPublicCorpus reads the dataset and its referenced public files.
func LoadPublicCorpus(path string) ([]Case, []rag.Document, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	var cases []Case
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, nil, "", err
	}
	if len(cases) == 0 {
		return nil, nil, "", fmt.Errorf("empty dataset")
	}
	seen := map[string]bool{}
	var docs []rag.Document
	for _, c := range cases {
		if c.ID == "" || c.Question == "" || c.SourceDoc == "" {
			return nil, nil, "", fmt.Errorf("incomplete case")
		}
		if seen[c.SourceDoc] {
			continue
		}
		seen[c.SourceDoc] = true
		body, err := os.ReadFile(c.SourceDoc)
		if err != nil {
			return nil, nil, "", fmt.Errorf("read %s: %w", c.SourceDoc, err)
		}
		docs = append(docs, rag.Document{Source: c.SourceDoc, Text: string(body)})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Source < docs[j].Source })
	h := sha256.New()
	h.Write(data)
	for _, d := range docs {
		h.Write([]byte(d.Source))
		h.Write([]byte(d.Text))
	}
	return cases, docs, hex.EncodeToString(h.Sum(nil)), nil
}

// ImproveRetrieval evaluates a candidate, checks the gate, and replaces local config.
func ImproveRetrieval(manager *config.Manager, name string, candidate config.Config, cases []Case, docs []rag.Document, hash string, before, after rag.Splitter, prediction Prediction) (RetrievalRun, RetrievalRun, Decision, error) {
	baseline := RunRetrieval(cases, docs, hash, before)
	next := RunRetrieval(cases, docs, hash, after)
	baseline = withHarness(baseline, RunHarness(cases, docs, before))
	next = withHarness(next, RunHarness(cases, docs, after))
	candidate.ChunkStrategy = after.Name()
	decision, err := Promote(manager, name, candidate,
		Result{Cases: len(cases), HitRate: baseline.HitRate, Quality: baseline.Quality},
		Result{Cases: len(cases), HitRate: next.HitRate, Quality: next.Quality, NewUnsafeFailures: maxInt(0, next.GroundingFailures-baseline.GroundingFailures)}, prediction)
	if err == nil {
		decision.Reason = "deterministic proxy gate passed; real LLM Judge not enabled"
	}
	return baseline, next, decision, err
}

func withHarness(run RetrievalRun, h HarnessRun) RetrievalRun {
	run.QualityMode, run.Quality, run.GroundingFailures, run.PassRate, run.EvaluatorScores = h.Mode, h.Quality, h.GroundingFailures, h.PassRate, h.Scores
	return run
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
