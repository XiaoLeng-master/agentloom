package evolution

import (
	"fmt"
	"math"

	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

// CandidateConfig is the concrete configuration a task would install.
type CandidateConfig struct {
	ChunkStrategy string `json:"chunk_strategy"`
	SplitterSize  int    `json:"splitter_size,omitempty"`
	Overlap       int    `json:"overlap,omitempty"`
	ChildSize     int    `json:"child_size,omitempty"`
	ParentSize    int    `json:"parent_size,omitempty"`
	TopK          int    `json:"top_k"`
	PromptMode    string `json:"prompt_mode"`
	ToolMode      string `json:"tool_mode"`
}

// OptimizationTask is the structured record that drives one candidate.
// Prediction is declared from the baseline failure rate before any retest.
type OptimizationTask struct {
	ID           string          `json:"id"`
	Version      int             `json:"version"`
	Layer        string          `json:"layer"`
	Cause        string          `json:"cause"`
	Action       string          `json:"action"`
	Rationale    string          `json:"rationale"`
	Config       CandidateConfig `json:"config"`
	SplitterName string          `json:"splitter"`
	SplitterSize int             `json:"splitter_size,omitempty"`
	Overlap      int             `json:"overlap,omitempty"`
	ChildSize    int             `json:"child_size,omitempty"`
	ParentSize   int             `json:"parent_size,omitempty"`
	TopK         int             `json:"top_k"`
	PromptMode   string          `json:"prompt_mode"`
	ToolMode     string          `json:"tool_mode"`
	Prediction   Prediction      `json:"prediction"`
}

// Apply returns the policy a candidate would retest. Unset fields keep the baseline.
func (t OptimizationTask) Apply(base Policy) Policy {
	policy := base
	policy.Name = t.ID
	if t.TopK > 0 {
		policy.TopK = t.TopK
	}
	if t.PromptMode != "" {
		policy.PromptMode = t.PromptMode
	}
	if t.ToolMode != "" {
		policy.ToolMode = t.ToolMode
	}
	if t.SplitterName != "" {
		policy.Splitter = splitterParams{
			name:    t.SplitterName,
			size:    t.SplitterSize,
			overlap: t.Overlap,
			child:   t.ChildSize,
			parent:  t.ParentSize,
		}.build()
	}
	return normalizePolicy(policy)
}

// GenerateTasks builds optimization candidates from attributed failures.
// It returns more than one candidate when more than one lever is available,
// and each candidate carries its own predicted interval.
func GenerateTasks(baseline Policy, report BatchReport) []OptimizationTask {
	baseline = normalizePolicy(baseline)
	counts := report.Metrics.FailuresByLayer
	if counts == nil {
		counts = map[string]int{}
		for _, failure := range report.Failures() {
			counts[failure.Layer]++
		}
	}
	cases := report.Metrics.Cases
	if cases <= 0 {
		cases = len(report.Results)
	}
	var tasks []OptimizationTask
	if counts[LayerRetrieval] > 0 && cases > 0 {
		failRate := float64(counts[LayerRetrieval]) / float64(cases)
		spec := splitterSpec(baseline.Splitter)
		for _, candidate := range retrievalSplitters() {
			if candidate.name == spec.name && candidate.size == spec.size && candidate.overlap == spec.overlap && candidate.child == spec.child && candidate.parent == spec.parent {
				continue
			}
			task := taskFrom(baseline, "retrieval-"+candidate.name, LayerRetrieval,
				fmt.Sprintf("将切分策略从 %s 换为 %s", spec.name, candidate.name))
			task.SplitterName = candidate.name
			task.SplitterSize = candidate.size
			task.Overlap = candidate.overlap
			task.ChildSize = candidate.child
			task.ParentSize = candidate.parent
			min, max := band(failRate, candidate.lo, candidate.hi)
			task.Prediction = Prediction{
				Metric:                  "hit_rate",
				ExpectedHitRateDeltaMin: min,
				ExpectedHitRateDeltaMax: max,
			}
			task.Rationale = fmt.Sprintf("检索层失败 %d/%d（占比 %.4f）。在复测前假设 %s 能挽回其中一部分来源命中，预期 HitRate 增量落在 [%.4f, %.4f]。",
				counts[LayerRetrieval], cases, failRate, candidate.name, min, max)
			tasks = append(tasks, task)
		}
		if baseline.TopK < 8 {
			task := taskFrom(baseline, "retrieval-topk", LayerRetrieval,
				fmt.Sprintf("将 TopK 从 %d 提高到 %d", baseline.TopK, baseline.TopK+2))
			task.TopK = baseline.TopK + 2
			min, max := band(failRate, 0.02, 0.22)
			task.Prediction = Prediction{
				Metric:                  "hit_rate",
				ExpectedHitRateDeltaMin: min,
				ExpectedHitRateDeltaMax: max,
			}
			task.Rationale = fmt.Sprintf("检索层失败 %d/%d。在复测前假设扩大 TopK 只能挽回接近边界的未命中，预期 HitRate 增量落在 [%.4f, %.4f]。",
				counts[LayerRetrieval], cases, min, max)
			tasks = append(tasks, task)
		}
	}
	if counts[LayerPrompt] > 0 && cases > 0 && baseline.PromptMode != PromptConcise {
		failRate := float64(counts[LayerPrompt]) / float64(cases)
		task := taskFrom(baseline, "prompt-concise", LayerPrompt, "将回答策略从原文复述改为按问句重合抽取短句并保留来源")
		task.PromptMode = PromptConcise
		min, max := band(failRate, 0.03, 0.40)
		task.Prediction = Prediction{
			Metric:                  "quality",
			ExpectedQualityDeltaMin: min,
			ExpectedQualityDeltaMax: max,
		}
		task.Rationale = fmt.Sprintf("Prompt 层失败 %d/%d（占比 %.4f）。在复测前假设简洁抽取能提高答案质量代理，预期质量增量落在 [%.4f, %.4f]。",
			counts[LayerPrompt], cases, failRate, min, max)
		tasks = append(tasks, task)
	}
	if counts[LayerTool] > 0 && cases > 0 && baseline.ToolMode != ToolSearch {
		failRate := float64(counts[LayerTool]) / float64(cases)
		task := taskFrom(baseline, "tool-search", LayerTool, "将工具策略改为调用 search_docs，并以完整问题作为 query")
		task.ToolMode = ToolSearch
		min, max := band(failRate, 0.50, 1.00)
		task.Prediction = Prediction{
			Metric:           "tool_success",
			ExpectedDeltaMin: min,
			ExpectedDeltaMax: max,
		}
		task.Rationale = fmt.Sprintf("工具层失败 %d/%d。在复测前假设改为 search_docs 能消除参数或调用错误，预期工具成功率增量落在 [%.4f, %.4f]。",
			counts[LayerTool], cases, min, max)
		tasks = append(tasks, task)
	}
	retrievalCause := dominantCause(report, LayerRetrieval, CauseRetrievalMiss)
	promptCause := dominantCause(report, LayerPrompt, CausePromptUnqualified)
	toolCause := dominantCause(report, LayerTool, CauseToolError)
	for i := range tasks {
		tasks[i].Version = i + 1
		switch tasks[i].Layer {
		case LayerRetrieval:
			tasks[i].Cause = retrievalCause
		case LayerPrompt:
			tasks[i].Cause = promptCause
		case LayerTool:
			tasks[i].Cause = toolCause
		}
		tasks[i].Config = CandidateConfig{
			ChunkStrategy: tasks[i].SplitterName,
			SplitterSize:  tasks[i].SplitterSize,
			Overlap:       tasks[i].Overlap,
			ChildSize:     tasks[i].ChildSize,
			ParentSize:    tasks[i].ParentSize,
			TopK:          tasks[i].TopK,
			PromptMode:    tasks[i].PromptMode,
			ToolMode:      tasks[i].ToolMode,
		}
	}
	return tasks
}

func dominantCause(report BatchReport, layer, fallback string) string {
	counts := map[string]int{}
	for _, failure := range report.Failures() {
		if failure.Layer == layer && failure.Cause != "" {
			counts[failure.Cause]++
		}
	}
	best := ""
	bestN := 0
	for cause, n := range counts {
		if n > bestN || (n == bestN && (best == "" || cause < best)) {
			best = cause
			bestN = n
		}
	}
	if best == "" {
		return fallback
	}
	return best
}

type retrievalChoice struct {
	splitterParams
	lo float64
	hi float64
}

func retrievalSplitters() []retrievalChoice {
	return []retrievalChoice{
		{splitterParams: splitterParams{name: "recursive", size: 60, overlap: 10}, lo: 0.12, hi: 0.55},
		{splitterParams: splitterParams{name: "semantic"}, lo: 0.04, hi: 0.40},
		{splitterParams: splitterParams{name: "parent-child", child: 20, parent: 80}, lo: 0.08, hi: 0.50},
	}
}

// PolicyForStrategy maps an active chunk strategy back to the deterministic baseline.
func PolicyForStrategy(strategy string) Policy {
	policy := BaselinePolicy()
	switch strategy {
	case "recursive":
		policy.Splitter = rag.RecursiveSplitter{Size: 60, Overlap: 10}
		policy.Name = "recursive-topk3-echo-search"
	case "semantic":
		policy.Splitter = rag.SemanticSplitter{}
		policy.Name = "semantic-topk3-echo-search"
	case "parent-child":
		policy.Splitter = rag.ParentChildSplitter{ChildSize: 20, ParentSize: 80}
		policy.Name = "parent-child-topk3-echo-search"
	default:
		policy.Splitter = rag.FixedSplitter{Size: 40}
		policy.Name = "fixed-topk3-echo-search"
	}
	return policy
}

func taskFrom(base Policy, id, layer, action string) OptimizationTask {
	spec := splitterSpec(base.Splitter)
	return OptimizationTask{
		ID:           id,
		Layer:        layer,
		Action:       action,
		SplitterName: spec.name,
		SplitterSize: spec.size,
		Overlap:      spec.overlap,
		ChildSize:    spec.child,
		ParentSize:   spec.parent,
		TopK:         base.TopK,
		PromptMode:   base.PromptMode,
		ToolMode:     base.ToolMode,
	}
}

func band(failRate, lo, hi float64) (float64, float64) {
	min := round4(lo * failRate)
	max := round4(hi * failRate)
	if max <= min {
		max = round4(min + 0.0001)
	}
	return min, max
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}
