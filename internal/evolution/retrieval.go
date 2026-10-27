package evolution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

// Case is one row of the fixed public evaluation set.
// Type and GroundTruth are optional for older retrieval-only callers.
type Case struct {
	ID          string `json:"id"`
	Type        string `json:"type,omitempty"`
	Question    string `json:"question"`
	GroundTruth string `json:"groundTruth,omitempty"`
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
	PromptMode        string             `json:"prompt_mode,omitempty"`
	TopK              int                `json:"top_k,omitempty"`
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

// LoadPublicCorpus reads the dataset and the files it names, relative to the
// current directory. make loop and POST /v1/evolve use this path, so editing
// those files changes their hash.
func LoadPublicCorpus(path string) ([]Case, []rag.Document, string, error) {
	return LoadPublicCorpusIn("", path)
}

// LoadPublicCorpusIn reads source files under root. An empty root uses the
// paths as written in the dataset. Document sources stay the dataset paths,
// so hit checks still compare against sourceDoc.
func LoadPublicCorpusIn(root, path string) ([]Case, []rag.Document, string, error) {
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
		sourcePath := c.SourceDoc
		if root != "" {
			sourcePath = filepath.Join(root, filepath.FromSlash(c.SourceDoc))
		}
		body, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, nil, "", fmt.Errorf("read %s: %w", sourcePath, err)
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

type frozenCorpus struct {
	Dataset string            `json:"dataset"`
	Files   map[string]string `json:"files"`
}

// LoadFrozenCorpus reads a snapshot written by the regression test fixture.
// The dataset text and file bodies are the bytes from the measurement, not the
// working tree.
func LoadFrozenCorpus(path string) ([]Case, []rag.Document, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	var frozen frozenCorpus
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return nil, nil, "", err
	}
	if frozen.Dataset == "" || len(frozen.Files) == 0 {
		return nil, nil, "", fmt.Errorf("empty frozen corpus")
	}
	var cases []Case
	if err := json.Unmarshal([]byte(frozen.Dataset), &cases); err != nil {
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
		body, ok := frozen.Files[c.SourceDoc]
		if !ok {
			return nil, nil, "", fmt.Errorf("frozen corpus missing %s", c.SourceDoc)
		}
		docs = append(docs, rag.Document{Source: c.SourceDoc, Text: body})
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Source < docs[j].Source })
	h := sha256.New()
	h.Write([]byte(frozen.Dataset))
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
