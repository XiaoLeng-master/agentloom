package evolution

import (
	"fmt"
	"strings"
)

// Markdown renders the closed-loop report for the command-line entrypoint.
func (r LoopReport) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 评测—归因—优化—复测门禁报告\n\n")
	fmt.Fprintf(&b, "- 数据集：%s\n", emptyDash(r.DatasetPath))
	fmt.Fprintf(&b, "- 条数：%d\n", r.Cases)
	fmt.Fprintf(&b, "- 数据集哈希：%s\n", r.DatasetHash)
	fmt.Fprintf(&b, "- 模型：确定性本地替身（不调用外部 LLM）\n\n")

	m := r.Baseline.Metrics
	fmt.Fprintf(&b, "## 基线批量评测\n\n")
	fmt.Fprintf(&b, "| 指标 | 值 |\n|---|---|\n")
	fmt.Fprintf(&b, "| 策略 | %s / %s / top-%d |\n", m.Strategy, m.PromptMode, m.TopK)
	fmt.Fprintf(&b, "| HitRate | %.4f |\n", m.HitRate)
	fmt.Fprintf(&b, "| 答案质量代理 | %.4f |\n", m.Quality)
	fmt.Fprintf(&b, "| 工具成功率 | %.4f |\n", m.ToolSuccess)
	fmt.Fprintf(&b, "| 失败样本 | %d |\n", m.FailureCount)
	fmt.Fprintf(&b, "| 归因分层 | prompt=%d，retrieval=%d，tool=%d |\n",
		m.FailuresByLayer[LayerPrompt], m.FailuresByLayer[LayerRetrieval], m.FailuresByLayer[LayerTool])
	fmt.Fprintf(&b, "\n四类评测器均分：")
	for i, name := range []string{"prompt", "rag", "skill", "agent"} {
		if i > 0 {
			b.WriteString("，")
		}
		fmt.Fprintf(&b, "%s=%.4f", name, m.EvaluatorMeans[name])
	}
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "## 失败聚类\n\n")
	if len(r.Clusters) == 0 {
		b.WriteString("没有失败样本。\n\n")
	} else {
		b.WriteString("| 层 | 原因 | 类型 | 来源 | 数量 | 样例 |\n|---|---|---|---|---|---|\n")
		for _, cluster := range r.Clusters {
			sample := strings.Join(cluster.CaseIDs, ",")
			if len(cluster.CaseIDs) > 3 {
				sample = strings.Join(cluster.CaseIDs[:3], ",") + "…"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s |\n",
				cluster.Layer, cluster.Cause, emptyDash(cluster.CaseType), emptyDash(cluster.SourceDoc), cluster.Count, sample)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## 失败样本\n\n")
	failures := r.Baseline.Failures()
	if len(failures) == 0 {
		b.WriteString("无。\n\n")
	} else {
		b.WriteString("| ID | 层 | 原因 | 信号 |\n|---|---|---|---|\n")
		for _, failure := range failures {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", failure.ID, failure.Layer, failure.Cause, strings.Join(failure.Signals, "; "))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## 优化候选与自动复测\n\n")
	if len(r.Candidates) == 0 {
		b.WriteString("没有生成候选。\n\n")
	} else {
		b.WriteString("| 候选 | 层 | 预测指标 | 预期区间 | 实际增量 | 预测成立 | 门禁 | 复测 HitRate | 复测质量 |\n|---|---|---|---|---|---|---|---|---|\n")
		for _, candidate := range r.Candidates {
			min, max := predictionBand(candidate.Task.Prediction)
			fmt.Fprintf(&b, "| %s | %s | %s | [%.4f, %.4f] | %.4f | %s | %s | %.4f | %.4f |\n",
				candidate.Task.ID, candidate.Task.Layer, metricName(candidate.Task.Prediction),
				min, max, candidate.ActualDelta, yesNo(candidate.PredictionMatched),
				gateLabel(candidate.Gate), candidate.Metrics.HitRate, candidate.Metrics.Quality)
		}
		b.WriteString("\n")
		for _, candidate := range r.Candidates {
			fmt.Fprintf(&b, "- `%s`：%s\n", candidate.Task.ID, candidate.Task.Rationale)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "## 门禁生效\n\n")
	if r.Promoted == nil {
		b.WriteString("没有候选替换本地配置。\n\n")
	} else {
		fmt.Fprintf(&b, "- 生效候选：%s\n", r.Promoted.TaskID)
		fmt.Fprintf(&b, "- 配置版本：%d\n", r.Promoted.Version)
		fmt.Fprintf(&b, "- 切分策略：%s\n", r.Promoted.ChunkStrategy)
		fmt.Fprintf(&b, "- Prompt 策略：%s\n", r.Promoted.PromptMode)
		fmt.Fprintf(&b, "- 工具策略：%s\n", r.Promoted.ToolMode)
		fmt.Fprintf(&b, "- TopK：%d\n\n", r.Promoted.TopK)
	}

	fmt.Fprintf(&b, "## 边界\n\n")
	for _, note := range r.Notes {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	return b.String()
}

func predictionBand(p Prediction) (float64, float64) {
	switch p.Metric {
	case "quality":
		return p.ExpectedQualityDeltaMin, p.ExpectedQualityDeltaMax
	case "tool_success":
		return p.ExpectedDeltaMin, p.ExpectedDeltaMax
	default:
		return p.ExpectedHitRateDeltaMin, p.ExpectedHitRateDeltaMax
	}
}

func metricName(p Prediction) string {
	if p.Metric == "" {
		return "hit_rate"
	}
	return p.Metric
}

func gateLabel(d Decision) string {
	if d.Passed {
		return "通过"
	}
	return "不通过"
}

func yesNo(v bool) string {
	if v {
		return "成立"
	}
	return "不成立"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
