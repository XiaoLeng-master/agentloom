package evolution

import (
	"errors"
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
	Results           []CaseOutcome    `json:"-"`
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
			"评测是离线确定性替身：自己检索并渲染回答，不经过 Chat 的 Planner/Executor/Verifier，也不是生成答案质量或 LLM Judge。",
			"词法代理 deterministic_proxy 用 groundTruth 与首个检索片段的词法重叠打分，并统计引用是否覆盖期望来源。它和来源命中、渲染回答里的事实覆盖是三项不同指标。",
			"归因读取模拟调用记录：工具报错、unknown tool、max iterations 归工具层；期望来源不在检索结果里，或来源命中但上下文没有 ground truth，归检索层；只有上下文含有 ground truth 而渲染回答没有时才归 Prompt 层。",
			"门禁要求来源命中不降、词法代理不降（1e-6）、不新增引用失败、归因失败数不增加，并且至少一项硬指标变好。把回答变短不能单独过门禁。预测未命中会记录，但不单独决定晋升。",
			"预测区间在复测之前由该层失败占比乘固定系数得到，不是历史校准值。",
			"未调用外部 LLM 或付费 API。公开基线的工具策略是 search，工具层失败只出现在注入的工具模式测试里。",
			"门禁通过后只把候选放进进程内灰度，不整进程 Reload。本命令不向 etcd 写入。设置 AGENTLOOM_ETCD_ENDPOINTS 后，可选的 EtcdSource 用同一套灰度方法接收外部配置。观测窗口里来源命中下降、词法代理下降、新增引用失败、归因失败数上升或引用失败数上升就自动回滚；持平或变好则按 10%、50%、100% 放量。语料包含 README 和部分源文件，改那些文件会改变数据集哈希和指标。",
		},
	}
	if baseReport.Metrics.FailuresByLayer[LayerTool] == 0 {
		report.Notes = append(report.Notes, "本次基线的模拟调用记录没有 tool error、unknown tool 或 max iterations exceeded，工具层失败数为 0。这三类信号由单元测试覆盖。")
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
		citationDelta := after.Metrics.GroundingFailures - baseReport.Metrics.GroundingFailures
		if citationDelta < 0 {
			citationDelta = 0
		}
		nextGate := after.Metrics.GateResult(citationDelta)
		decision, err := DecideCandidate(baseGate, nextGate, task.Prediction)
		if err != nil && !errors.Is(err, ErrRejected) {
			return report, err
		}
		delta, _, _ := predictedDelta(baseGate, nextGate, task.Prediction)
		candidate := CandidateResult{
			Task:              task,
			Metrics:           after.Metrics,
			ActualDelta:       delta,
			PredictionMatched: decision.PredictionMatched,
			Gate:              decision,
			NewUnsafeFailures: citationDelta,
			Results:           after.Results,
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
	if percent, active := opts.Manager.CanaryPercent(opts.AgentName); active {
		report.Notes = append(report.Notes, fmt.Sprintf("已有 %d%% 灰度在进行。本次基线按稳定配置复测，不重复启动候选。", percent))
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
	if a.Metrics.FailureCount != b.Metrics.FailureCount {
		return a.Metrics.FailureCount < b.Metrics.FailureCount
	}
	if a.Metrics.HitRate != b.Metrics.HitRate {
		return a.Metrics.HitRate > b.Metrics.HitRate
	}
	if a.Metrics.Quality != b.Metrics.Quality {
		return a.Metrics.Quality > b.Metrics.Quality
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
