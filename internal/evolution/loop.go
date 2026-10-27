package evolution

import (
	"fmt"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/rag"
)

// LoopOptions configures one closed-loop run. Baseline defaults to the
// deterministic demo policy when unset.
type LoopOptions struct {
	DatasetPath string
	Cases       []Case
	Docs        []rag.Document
	DatasetHash string
	Baseline    Policy
	Manager     *config.Manager
	AgentName   string
	History     *History
}

// CandidateResult is the automatic retest of one optimization task.
type CandidateResult struct {
	Task              OptimizationTask `json:"task"`
	Metrics           BatchMetrics     `json:"metrics"`
	ActualDelta       float64          `json:"actual_delta"`
	PredictionMatched bool             `json:"prediction_matched"`
	Gate              Decision         `json:"gate"`
	NewUnsafeFailures int              `json:"new_unsafe_failures"`
}

// Promotion is the candidate installed into the local config manager.
type Promotion struct {
	TaskID        string `json:"task_id"`
	Version       int    `json:"version"`
	ChunkStrategy string `json:"chunk_strategy"`
	PromptMode    string `json:"prompt_mode"`
	ToolMode      string `json:"tool_mode"`
	TopK          int    `json:"top_k"`
	Percent       int    `json:"percent,omitempty"`
	Stage         string `json:"stage,omitempty"`
}

// LoopReport is the end-to-end record of evaluate, attribute, optimize, and retest.
type LoopReport struct {
	DatasetPath string            `json:"dataset_path"`
	DatasetHash string            `json:"dataset_hash"`
	Cases       int               `json:"cases"`
	Baseline    BatchReport       `json:"baseline"`
	Clusters    []Cluster         `json:"clusters"`
	Candidates  []CandidateResult `json:"candidates"`
	Tasks       []TaskRecord      `json:"tasks"`
	CauseCounts []CauseCount      `json:"cause_counts"`
	Promoted    *Promotion        `json:"promoted,omitempty"`
	Rollout     *RolloutEvent     `json:"rollout,omitempty"`
	Notes       []string          `json:"notes"`
}

// TaskRecord is the optimization task plus the retest outcome.
// The task fields are fixed before retest; the outcome fields are filled after.
type TaskRecord struct {
	ID                string          `json:"id"`
	Version           int             `json:"version"`
	Layer             string          `json:"layer"`
	Cause             string          `json:"cause"`
	Action            string          `json:"action"`
	Config            CandidateConfig `json:"config"`
	Prediction        Prediction      `json:"prediction"`
	ActualDelta       float64         `json:"actual_delta"`
	PredictionMatched bool            `json:"prediction_matched"`
	Passed            bool            `json:"passed"`
	GateReason        string          `json:"gate_reason"`
}

// RunLoop evaluates the fixed set, clusters failures, generates candidates,
// and retests each candidate. A gate pass starts a canary instead of reloading
// the stable config for every request.
func RunLoop(opts LoopOptions) (LoopReport, error) {
	if len(opts.Cases) == 0 {
		return LoopReport{}, fmt.Errorf("empty evaluation set")
	}
	if opts.DatasetHash == "" {
		return LoopReport{}, fmt.Errorf("missing dataset hash")
	}
	if opts.AgentName == "" {
		opts.AgentName = "kb-agent"
	}
	baseline := normalizePolicy(opts.Baseline)
	if opts.Baseline.Splitter == nil && opts.Baseline.Name == "" && opts.Baseline.TopK == 0 {
		baseline = BaselinePolicy()
	}
	cache := newIndexCache(opts.Docs)
	baseReport := scoreBatch(opts.Cases, opts.DatasetHash, baseline, cache.get(baseline))
	clusters := ClusterFailures(baseReport.Failures())
	tasks := GenerateTasks(baseline, baseReport)
	report := LoopReport{
		DatasetPath: opts.DatasetPath,
		DatasetHash: opts.DatasetHash,
		Cases:       len(opts.Cases),
		Baseline:    baseReport,
		Clusters:    clusters,
		Tasks:       []TaskRecord{},
		CauseCounts: CountByCause(baseReport.Failures()),
		Notes: []string{
			"答案质量是确定性代理：事实覆盖、简洁度、引用的加权，不是 LLM Judge。不合格指检索已命中时期望事实没有完整出现在答案里。",
			"归因读取 Trace 信号：工具报错、unknown tool、max iterations exceeded 归工具层；期望来源不在检索结果里归检索层；来源命中但答案不合格归 Prompt 层。",
			"每条样本另外经过 prompt / rag / skill / agent 四个规则评测器。PromptEvaluator 的空白分词 Jaccard 不参与分层，因为公开集以中文为主。",
			"预测区间在复测之前由该层失败占比声明，复测之后才记录实际增量是否落在区间内。",
			"未调用外部 LLM 或付费 API。执行器是确定性替身：echo 复述检索片段，concise 按问句重合抽取短句。",
			"门禁通过后只把候选放进进程内灰度，不整进程 Reload，不写 etcd。放量和回滚由后续观测窗口决定。",
		},
	}
	if baseReport.Metrics.FailuresByLayer[LayerTool] == 0 {
		report.Notes = append(report.Notes, "本次基线 Trace 没有 tool error、unknown tool 或 max iterations exceeded，工具层失败数为 0。这三类信号由单元测试覆盖。")
	}
	if len(tasks) == 0 {
		report.Notes = append(report.Notes, "归因结果没有产生可执行的优化候选。")
		return report, nil
	}
	baseGate := baseReport.Metrics.GateResult(0)
	var best *CandidateResult
	for _, task := range tasks {
		policy := task.Apply(baseline)
		after := scoreBatch(opts.Cases, opts.DatasetHash, policy, cache.get(policy))
		unsafe := newUnsafeCount(baseReport, after)
		nextGate := after.Metrics.GateResult(unsafe)
		decision, _ := Judge(baseGate, nextGate, task.Prediction)
		delta, _, _ := predictedDelta(baseGate, nextGate, task.Prediction)
		candidate := CandidateResult{
			Task:              task,
			Metrics:           after.Metrics,
			ActualDelta:       delta,
			PredictionMatched: decision.PredictionMatched,
			Gate:              decision,
			NewUnsafeFailures: unsafe,
		}
		report.Candidates = append(report.Candidates, candidate)
		report.Tasks = append(report.Tasks, TaskRecord{
			ID:                task.ID,
			Version:           task.Version,
			Layer:             task.Layer,
			Cause:             task.Cause,
			Action:            task.Action,
			Config:            task.Config,
			Prediction:        task.Prediction,
			ActualDelta:       candidate.ActualDelta,
			PredictionMatched: candidate.PredictionMatched,
			Passed:            decision.Passed,
			GateReason:        decision.Reason,
		})
		if decision.Passed && (best == nil || betterCandidate(candidate, *best)) {
			copy := candidate
			best = &copy
		}
	}
	if best == nil {
		report.Notes = append(report.Notes, "全部候选未通过复测门禁，本地配置保持基线。")
		return report, nil
	}
	if opts.Manager == nil {
		report.Notes = append(report.Notes, "有候选通过门禁，但没有配置管理器，因此没有进入灰度。")
		report.Promoted = &Promotion{
			TaskID:        best.Task.ID,
			ChunkStrategy: best.Task.SplitterName,
			PromptMode:    best.Task.PromptMode,
			ToolMode:      best.Task.ToolMode,
			TopK:          best.Task.TopK,
		}
		return report, nil
	}
	cfg := config.Config{
		Name:          opts.AgentName,
		SystemPrompt:  fmt.Sprintf("Closed-loop candidate %s. %s", best.Task.ID, best.Task.Action),
		ChunkStrategy: best.Task.SplitterName,
		PromptMode:    best.Task.PromptMode,
		TopK:          best.Task.TopK,
		Tools:         []string{"search_docs"},
	}
	rollout := NewRollout(opts.Manager, opts.AgentName)
	rollout.History = opts.History
	event, err := rollout.Start(cfg)
	if err != nil {
		return report, err
	}
	report.Rollout = &event
	report.Promoted = &Promotion{
		TaskID:        best.Task.ID,
		Version:       event.CanaryVersion,
		ChunkStrategy: best.Task.SplitterName,
		PromptMode:    best.Task.PromptMode,
		ToolMode:      best.Task.ToolMode,
		TopK:          best.Task.TopK,
		Percent:       event.Percent,
		Stage:         "canary",
	}
	report.Notes = append(report.Notes, "候选已进入灰度，没有整进程 Reload。未命中租户哈希桶的请求继续使用稳定配置；在途请求保持拿到时的配置。放量和回滚要等观测窗口。")
	return report, nil
}

func betterCandidate(a, b CandidateResult) bool {
	if a.Metrics.Quality != b.Metrics.Quality {
		return a.Metrics.Quality > b.Metrics.Quality
	}
	if a.Metrics.HitRate != b.Metrics.HitRate {
		return a.Metrics.HitRate > b.Metrics.HitRate
	}
	return a.Task.Version < b.Task.Version
}

func newUnsafeCount(base, next BatchReport) int {
	prev := make(map[string]bool, len(base.Results))
	for _, result := range base.Results {
		prev[result.ID] = result.SafetyPass
	}
	n := 0
	for _, result := range next.Results {
		if prev[result.ID] && !result.SafetyPass {
			n++
		}
	}
	return n
}
