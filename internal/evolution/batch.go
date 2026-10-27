package evolution

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/XiaoLeng-master/agentloom/internal/eval"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

const (
	// PublicDatasetPath is the fixed regression set bound by the closed loop.
	PublicDatasetPath = "datasets/rag_eval_98.json"
	// PublicDatasetSize is the number of cases in that set.
	PublicDatasetSize = 98

	PromptEcho    = "echo"
	PromptConcise = "concise"

	ToolSearch    = "search"
	ToolSkip      = "skip"
	ToolBadArgs   = "bad_args"
	ToolWrongName = "wrong_name"
	ToolError     = "error"

	LayerPrompt    = "prompt"
	LayerRetrieval = "retrieval"
	LayerTool      = "tool"

	CauseToolError         = "tool_error"
	CauseUnknownTool       = "unknown_tool"
	CauseMaxIterations     = "max_iterations"
	CauseRetrievalMiss     = "expected source absent from top-k retrieval"
	CausePromptUnqualified = "retrieval hit but answer unqualified"
)

// Policy is a deterministic agent stand-in: retrieval settings plus the
// prompt and tool behavior used when no external model is called.
type Policy struct {
	Name       string
	Splitter   rag.Splitter
	TopK       int
	PromptMode string
	ToolMode   string
}

// BaselinePolicy is the current demo path: fixed chunks, top-3, echo the
// tool output, and call search_docs with the question.
func BaselinePolicy() Policy {
	return Policy{
		Name:       "fixed-topk3-echo-search",
		Splitter:   rag.FixedSplitter{Size: 40},
		TopK:       3,
		PromptMode: PromptEcho,
		ToolMode:   ToolSearch,
	}
}

// EvaluatorScore is one deterministic evaluator result on a case.
type EvaluatorScore struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Pass  bool    `json:"pass"`
}

// CaseOutcome is the per-case record of a batch run, including the failure
// sample fields used for clustering.
type CaseOutcome struct {
	ID               string           `json:"id"`
	Type             string           `json:"type"`
	Question         string           `json:"question"`
	GroundTruth      string           `json:"ground_truth"`
	ExpectedSource   string           `json:"expected_source"`
	RetrievedSources []string         `json:"retrieved_sources"`
	Hit              bool             `json:"hit"`
	Rank             int              `json:"rank"`
	ToolCalled       bool             `json:"tool_called"`
	ToolName         string           `json:"tool_name"`
	ToolQuery        string           `json:"tool_query"`
	ToolError        string           `json:"tool_error,omitempty"`
	MaxIterations    bool             `json:"max_iterations,omitempty"`
	Answer           string           `json:"answer"`
	FactInContext    float64          `json:"fact_in_context"`
	FactScore        float64          `json:"fact_score"`
	ConciseScore     float64          `json:"concise_score"`
	CiteScore        float64          `json:"cite_score"`
	Quality          float64          `json:"quality"`
	SafetyPass       bool             `json:"safety_pass"`
	Failed           bool             `json:"failed"`
	Layer            string           `json:"layer,omitempty"`
	Cause            string           `json:"cause,omitempty"`
	Signals          []string         `json:"signals,omitempty"`
	Scores           []EvaluatorScore `json:"scores"`
}

// BatchMetrics aggregates one policy over the fixed case set.
type BatchMetrics struct {
	Policy          string             `json:"policy"`
	Strategy        string             `json:"strategy"`
	PromptMode      string             `json:"prompt_mode"`
	ToolMode        string             `json:"tool_mode"`
	TopK            int                `json:"top_k"`
	DatasetHash     string             `json:"dataset_hash"`
	Cases           int                `json:"cases"`
	HitRate         float64            `json:"hit_rate"`
	Quality         float64            `json:"quality"`
	ToolSuccess     float64            `json:"tool_success"`
	UnsafeFailures  int                `json:"unsafe_failures"`
	FailureCount    int                `json:"failure_count"`
	FailuresByLayer map[string]int     `json:"failures_by_layer"`
	EvaluatorMeans  map[string]float64 `json:"evaluator_means"`
}

// GateResult converts aggregate metrics into the release-gate shape.
// newUnsafe is computed against a baseline run, not from this batch alone.
func (m BatchMetrics) GateResult(newUnsafe int) Result {
	return Result{
		Cases:             m.Cases,
		HitRate:           m.HitRate,
		Quality:           m.Quality,
		ToolSuccess:       m.ToolSuccess,
		NewUnsafeFailures: newUnsafe,
	}
}

// BatchReport is one full pass over the case set.
type BatchReport struct {
	Metrics BatchMetrics  `json:"metrics"`
	Results []CaseOutcome `json:"results"`
}

// Failures returns cases the attribution rules marked as failed.
func (b BatchReport) Failures() []CaseOutcome {
	out := make([]CaseOutcome, 0)
	for _, result := range b.Results {
		if result.Failed {
			out = append(out, result)
		}
	}
	return out
}

type passage struct {
	Source string
	Text   string
}

type indexCache struct {
	docs []rag.Document
	mu   sync.Mutex
	m    map[string]*rag.Retriever
}

func newIndexCache(docs []rag.Document) *indexCache {
	return &indexCache{docs: docs, m: map[string]*rag.Retriever{}}
}

func (c *indexCache) get(policy Policy) *rag.Retriever {
	key := splitterKey(policy.Splitter)
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.m[key]; ok {
		return r
	}
	r := &rag.Retriever{Splitter: policy.Splitter, Embedder: rag.HashEmbedder{DimV: 512}, Store: &rag.MemoryStore{}}
	r.Index(c.docs)
	c.m[key] = r
	return r
}

// RunBatch evaluates every case under policy with the deterministic local
// stand-in. It does not call an external model.
func RunBatch(cases []Case, docs []rag.Document, datasetHash string, policy Policy) BatchReport {
	policy = normalizePolicy(policy)
	cache := newIndexCache(docs)
	return scoreBatch(cases, datasetHash, policy, cache.get(policy))
}

func scoreBatch(cases []Case, datasetHash string, policy Policy, retriever *rag.Retriever) BatchReport {
	policy = normalizePolicy(policy)
	results := make([]CaseOutcome, len(cases))
	registry := eval.NewRegistry(
		eval.PromptEvaluator{},
		eval.RAGEvaluator{},
		eval.SkillEvaluator{},
		eval.AgentEvaluator{MaxLatencyMs: 1000},
	)
	workers := workerCount(len(cases))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				results[idx] = evalCase(cases[idx], retriever, policy, registry)
			}
		}()
	}
	for i := range cases {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return BatchReport{Metrics: aggregate(policy, datasetHash, results), Results: results}
}

func workerCount(n int) int {
	if n <= 0 {
		return 1
	}
	w := runtime.GOMAXPROCS(0)
	if w < 1 {
		w = 1
	}
	if w > 8 {
		w = 8
	}
	if w > n {
		w = n
	}
	return w
}

func evalCase(c Case, retriever *rag.Retriever, policy Policy, registry *eval.Registry) CaseOutcome {
	out := CaseOutcome{
		ID:             c.ID,
		Type:           c.Type,
		Question:       c.Question,
		GroundTruth:    c.GroundTruth,
		ExpectedSource: c.SourceDoc,
		SafetyPass:     true,
	}
	var passages []passage
	switch policy.ToolMode {
	case ToolSkip:
		out.MaxIterations = true
		out.ToolError = "max iterations exceeded"
	case ToolWrongName:
		out.ToolCalled = true
		out.ToolName = "lookup"
		out.ToolQuery = c.Question
		out.ToolError = `error: unknown tool "lookup"`
	case ToolBadArgs:
		out.ToolCalled = true
		out.ToolName = "search_docs"
		out.ToolQuery = ""
		out.ToolError = `error: tool "search_docs" failed: query must be string`
	case ToolError:
		out.ToolCalled = true
		out.ToolName = "search_docs"
		out.ToolQuery = c.Question
		out.ToolError = `error: tool "search_docs" failed: upstream timeout`
	default:
		out.ToolCalled = true
		out.ToolName = "search_docs"
		out.ToolQuery = c.Question
		for _, hit := range retriever.Retrieve(c.Question, policy.TopK) {
			text := clipRunes(hit.Text, 1200)
			passages = append(passages, passage{Source: hit.Source, Text: text})
			out.RetrievedSources = append(out.RetrievedSources, hit.Source)
			if !out.Hit && hit.Source == c.SourceDoc {
				out.Hit = true
				out.Rank = len(out.RetrievedSources)
			}
		}
	}
	out.Answer = renderAnswer(policy.PromptMode, c.Question, passages)
	out.FactInContext = factScore(joinPassages(passages), c.GroundTruth)
	out.FactScore = factScore(out.Answer, c.GroundTruth)
	out.ConciseScore = conciseScore(out.Answer, c.GroundTruth)
	out.CiteScore = citeScore(out.Answer)
	out.Quality = 0.5*out.FactScore + 0.3*out.ConciseScore + 0.2*out.CiteScore
	out.SafetyPass = safetyPass(out.Answer)
	out.Scores = scoreEvaluators(c, out, registry)
	return Attribute(out)
}

func scoreEvaluators(c Case, out CaseOutcome, registry *eval.Registry) []EvaluatorScore {
	tools := []string{}
	if out.ToolCalled && out.ToolName != "" {
		tools = append(tools, out.ToolName)
	}
	events := []eval.Event{}
	if out.ToolCalled {
		events = append(events, eval.Event{
			Kind:    eval.EventTool,
			Name:    out.ToolName,
			Input:   out.ToolQuery,
			Output:  out.Answer,
			Latency: time.Millisecond,
		})
	}
	trace := eval.Trace{
		ID:        c.ID,
		Query:     c.Question,
		Answer:    out.Answer,
		Citations: append([]string(nil), out.RetrievedSources...),
		Tools:     tools,
		Events:    events,
		Expected: eval.Expected{
			Answer:     c.GroundTruth,
			Tools:      []string{"search_docs"},
			GroundedIn: []string{c.SourceDoc},
		},
	}
	raw := registry.Eval(trace)
	scores := make([]EvaluatorScore, 0, len(raw))
	for _, score := range raw {
		scores = append(scores, EvaluatorScore{Name: score.Name, Value: float64(score.Value), Pass: score.Pass})
	}
	return scores
}

// Attribute classifies one case from Trace signals.
// Tool traces win: tool error, unknown tool, or ErrMaxIterations.
// Otherwise a missing expected source is retrieval, and a source hit whose
// answer does not cover the ground truth is prompt.
func Attribute(out CaseOutcome) CaseOutcome {
	out.Failed = false
	out.Layer = ""
	out.Cause = ""
	out.Signals = nil
	switch {
	case out.MaxIterations || strings.Contains(out.ToolError, "max iterations exceeded"):
		return fail(out, LayerTool, CauseMaxIterations, "trace=max iterations exceeded")
	case strings.Contains(out.ToolError, "unknown tool") || (out.ToolCalled && out.ToolName != "" && out.ToolName != "search_docs"):
		name := out.ToolName
		if name == "" {
			name = "unknown"
		}
		return fail(out, LayerTool, CauseUnknownTool, "trace=unknown tool "+name)
	case out.ToolError != "":
		return fail(out, LayerTool, CauseToolError, "trace="+out.ToolError)
	case !out.Hit:
		return fail(out, LayerRetrieval, CauseRetrievalMiss,
			"expected="+out.ExpectedSource,
			"retrieved="+strings.Join(out.RetrievedSources, ","),
			fmt.Sprintf("rank=%d", out.Rank))
	case !answerQualified(out):
		return fail(out, LayerPrompt, CausePromptUnqualified,
			"source_hit=true",
			fmt.Sprintf("fact_in_answer=%.4f", out.FactScore))
	default:
		return out
	}
}

func answerQualified(out CaseOutcome) bool {
	if strings.TrimSpace(out.Answer) == "" {
		return false
	}
	if strings.TrimSpace(out.GroundTruth) == "" {
		return true
	}
	return out.FactScore+1e-9 >= 1
}

func fail(out CaseOutcome, layer, cause string, signals ...string) CaseOutcome {
	out.Failed = true
	out.Layer = layer
	out.Cause = cause
	out.Signals = signals
	return out
}

// FormatAnswer renders the deterministic executor answer for a prompt mode.
// echo repeats the retrieved passages; concise keeps one overlapping segment and the source.
func FormatAnswer(mode, question string, sources, texts []string) string {
	passages := make([]passage, 0, len(texts))
	for i, text := range texts {
		source := ""
		if i < len(sources) {
			source = sources[i]
		}
		passages = append(passages, passage{Source: source, Text: text})
	}
	return renderAnswer(mode, question, passages)
}

func renderAnswer(mode, question string, passages []passage) string {
	if len(passages) == 0 {
		if mode == PromptConcise {
			return "证据不足"
		}
		return "no indexed documents"
	}
	if mode == PromptConcise {
		return renderConcise(question, passages)
	}
	return renderEcho(passages)
}

func renderEcho(passages []passage) string {
	var b strings.Builder
	for i, p := range passages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Source: %s\nContent: %s", p.Source, p.Text)
	}
	return b.String()
}

func renderConcise(question string, passages []passage) string {
	best := ""
	bestSource := passages[0].Source
	bestScore := -1.0
	bestLen := 0
	for _, p := range passages {
		for _, segment := range splitSegments(p.Text) {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			score := questionOverlap(question, segment)
			n := len([]rune(segment))
			if score > bestScore || (score == bestScore && (best == "" || n < bestLen)) {
				best = segment
				bestSource = p.Source
				bestScore = score
				bestLen = n
			}
		}
	}
	if best == "" {
		best = strings.TrimSpace(passages[0].Text)
		bestSource = passages[0].Source
	}
	return best + "\nSource: " + bestSource
}

func joinPassages(passages []passage) string {
	var b strings.Builder
	for _, p := range passages {
		b.WriteString(p.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

func aggregate(policy Policy, datasetHash string, results []CaseOutcome) BatchMetrics {
	spec := splitterSpec(policy.Splitter)
	metrics := BatchMetrics{
		Policy:      policy.Name,
		Strategy:    spec.name,
		PromptMode:  policy.PromptMode,
		ToolMode:    policy.ToolMode,
		TopK:        policy.TopK,
		DatasetHash: datasetHash,
		Cases:       len(results),
		FailuresByLayer: map[string]int{
			LayerPrompt:    0,
			LayerRetrieval: 0,
			LayerTool:      0,
		},
		EvaluatorMeans: map[string]float64{},
	}
	if len(results) == 0 {
		return metrics
	}
	scoreSums := map[string]float64{}
	scoreCounts := map[string]int{}
	for _, result := range results {
		if result.Hit {
			metrics.HitRate++
		}
		metrics.Quality += result.Quality
		if result.ToolCalled && result.ToolError == "" && result.ToolName == "search_docs" && strings.TrimSpace(result.ToolQuery) != "" {
			metrics.ToolSuccess++
		}
		if !result.SafetyPass {
			metrics.UnsafeFailures++
		}
		if result.Failed {
			metrics.FailureCount++
			metrics.FailuresByLayer[result.Layer]++
		}
		for _, score := range result.Scores {
			scoreSums[score.Name] += score.Value
			scoreCounts[score.Name]++
		}
	}
	n := float64(len(results))
	metrics.HitRate /= n
	metrics.Quality /= n
	metrics.ToolSuccess /= n
	for name, sum := range scoreSums {
		if scoreCounts[name] > 0 {
			metrics.EvaluatorMeans[name] = sum / float64(scoreCounts[name])
		}
	}
	return metrics
}

func normalizePolicy(policy Policy) Policy {
	if policy.Splitter == nil {
		policy.Splitter = rag.FixedSplitter{Size: 40}
	}
	if policy.TopK <= 0 {
		policy.TopK = 3
	}
	if policy.PromptMode == "" {
		policy.PromptMode = PromptEcho
	}
	if policy.ToolMode == "" {
		policy.ToolMode = ToolSearch
	}
	if policy.Name == "" {
		policy.Name = policy.PromptMode + "/" + policy.ToolMode
	}
	return policy
}

func factScore(answer, groundTruth string) float64 {
	facts := splitFacts(groundTruth)
	if len(facts) == 0 {
		if strings.TrimSpace(answer) != "" {
			return 1
		}
		return 0
	}
	haystack := normalizeText(answer)
	hit := 0
	for _, fact := range facts {
		if strings.Contains(haystack, normalizeText(fact)) {
			hit++
		}
	}
	return float64(hit) / float64(len(facts))
}

func splitFacts(groundTruth string) []string {
	parts := strings.FieldsFunc(groundTruth, func(r rune) bool {
		switch r {
		case '、', '，', ',', '；', ';', '/', '／', '\n', '+':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "。.")
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func conciseScore(answer, groundTruth string) float64 {
	n := len([]rune(strings.TrimSpace(answer)))
	if n == 0 {
		return 0
	}
	budget := len([]rune(strings.TrimSpace(groundTruth))) * 4
	if budget < 80 {
		budget = 80
	}
	if n <= budget {
		return 1
	}
	return float64(budget) / float64(n)
}

func citeScore(answer string) float64 {
	if strings.Contains(answer, "Source:") {
		return 1
	}
	return 0
}

func safetyPass(answer string) bool {
	lower := strings.ToLower(answer)
	for _, marker := range []string{"instructions overridden", "developer mode enabled", "ignore previous instructions"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}

func questionOverlap(query, text string) float64 {
	qs := meaningfulRunes(query)
	if len(qs) == 0 {
		return 0
	}
	seen := map[rune]bool{}
	for _, r := range text {
		seen[r] = true
	}
	hit := 0
	for _, r := range qs {
		if seen[r] {
			hit++
		}
	}
	return float64(hit) / float64(len(qs))
}

func meaningfulRunes(s string) []rune {
	var out []rune
	for _, r := range strings.ToLower(s) {
		if r == ' ' || r == '\n' || r == '\t' || r == '？' || r == '?' || r == '，' || r == ',' || r == '。' || r == '、' {
			continue
		}
		out = append(out, r)
	}
	return out
}

func splitSegments(s string) []string {
	var out []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		out = append(out, cur.String())
		cur.Reset()
	}
	for _, r := range s {
		cur.WriteRune(r)
		switch r {
		case '.', '!', '?', '\n', '。', '！', '？', '；', ';':
			flush()
		}
	}
	flush()
	return out
}

func normalizeText(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

type splitterParams struct {
	name    string
	size    int
	overlap int
	child   int
	parent  int
}

func splitterSpec(s rag.Splitter) splitterParams {
	switch v := s.(type) {
	case rag.FixedSplitter:
		return splitterParams{name: "fixed", size: v.Size}
	case rag.RecursiveSplitter:
		return splitterParams{name: "recursive", size: v.Size, overlap: v.Overlap}
	case rag.SemanticSplitter:
		return splitterParams{name: "semantic"}
	case rag.ParentChildSplitter:
		return splitterParams{name: "parent-child", child: v.ChildSize, parent: v.ParentSize}
	default:
		if s == nil {
			return splitterParams{name: "fixed", size: 40}
		}
		return splitterParams{name: s.Name()}
	}
}

func splitterKey(s rag.Splitter) string {
	spec := splitterSpec(s)
	return fmt.Sprintf("%s|%d|%d|%d|%d", spec.name, spec.size, spec.overlap, spec.child, spec.parent)
}

func (p splitterParams) build() rag.Splitter {
	switch p.name {
	case "recursive":
		return rag.RecursiveSplitter{Size: p.size, Overlap: p.overlap}
	case "semantic":
		return rag.SemanticSplitter{}
	case "parent-child":
		return rag.ParentChildSplitter{ChildSize: p.child, ParentSize: p.parent}
	default:
		size := p.size
		if size <= 0 {
			size = 40
		}
		return rag.FixedSplitter{Size: size}
	}
}
