// Command canary reproduces one in-process ramp and one rollback.
// Both observation windows are inputs to this command. They are not measurements
// of datasets/rag_eval_98.json and they are not online traffic.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/XiaoLeng-master/agentloom/internal/config"
	"github.com/XiaoLeng-master/agentloom/internal/evolution"
)

func main() {
	dir, err := os.MkdirTemp("", "agentloom-canary-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	text, err := execute(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(text)
}

func execute(dir string) (string, error) {
	var b strings.Builder
	b.WriteString("# 进程内灰度复现\n\n")
	b.WriteString("两个场景的观测窗口都是本命令给定的输入，不是 datasets/rag_eval_98.json 的测量，也不是在线流量。\n\n")
	history := &evolution.History{Dir: filepath.Join(dir, "history")}

	rampStable := evolution.Result{Cases: 20, HitRate: 0.40, Quality: 0.50}
	rampCanary := evolution.Result{Cases: 20, HitRate: 0.46, Quality: 0.55}
	if err := runRamp(&b, history, rampStable, rampCanary); err != nil {
		return "", err
	}
	rollbackStable := evolution.Result{Cases: 20, HitRate: 0.40, Quality: 0.50}
	rollbackCanary := evolution.Result{Cases: 20, HitRate: 0.30, Quality: 0.40}
	if err := runRollback(&b, history, rollbackStable, rollbackCanary); err != nil {
		return "", err
	}
	experiments, err := history.List()
	if err != nil {
		return "", err
	}
	events, err := history.ListRollouts()
	if err != nil {
		return "", err
	}
	if len(experiments) != 0 || len(events) != 5 {
		return "", fmt.Errorf("history experiments=%d rollouts=%d", len(experiments), len(events))
	}
	fmt.Fprintf(&b, "## 历史\n\n实验记录 %d 条，灰度事件 %d 条。灰度事件写在 rollouts 子目录，不会被当成实验。\n", len(experiments), len(events))
	return b.String(), nil
}

func runRamp(b *strings.Builder, history *evolution.History, stable, canary evolution.Result) error {
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "stable-ramp", ChunkStrategy: "fixed", TopK: 3})
	rollout := evolution.NewRollout(mgr, "kb-agent")
	rollout.History = history
	started, err := rollout.Start(config.Config{Name: "kb-agent", SystemPrompt: "canary-ramp", ChunkStrategy: "recursive", TopK: 5})
	if err != nil {
		return err
	}
	if started.Kind != evolution.RolloutCanary || started.Percent != 10 || started.StableVersion != 1 || started.CanaryVersion != 2 {
		return fmt.Errorf("ramp start: %+v", started)
	}
	mid, err := rollout.Observe(stable, canary)
	if err != nil {
		return err
	}
	if mid.Kind != evolution.RolloutRamp || mid.Percent != 50 || mid.StableVersion != 1 || mid.CanaryVersion != 2 {
		return fmt.Errorf("ramp mid: %+v", mid)
	}
	done, err := rollout.Observe(stable, canary)
	if err != nil {
		return err
	}
	if done.Kind != evolution.RolloutRamp || done.Percent != 100 || done.StableVersion != 1 || done.CanaryVersion != 2 {
		return fmt.Errorf("ramp done: %+v", done)
	}
	active, err := mgr.Acquire("kb-agent")
	if err != nil {
		return err
	}
	defer active.Release()
	cfg := active.Config()
	if cfg.Version != 2 || cfg.ChunkStrategy != "recursive" || cfg.TopK != 5 || cfg.SystemPrompt != "canary-ramp" {
		return fmt.Errorf("ramp acquire: %+v", cfg)
	}
	fmt.Fprintf(b, "## 灰度→放量\n\n")
	fmt.Fprintf(b, "- 给定稳定窗口：样本 %d，来源命中 %.4f，词法代理 %.4f\n", stable.Cases, stable.HitRate, stable.Quality)
	fmt.Fprintf(b, "- 给定灰度窗口：样本 %d，来源命中 %.4f，词法代理 %.4f\n", canary.Cases, canary.HitRate, canary.Quality)
	fmt.Fprintf(b, "- 启动：%s %d%%（稳定版本 %d，灰度版本 %d）\n", started.Kind, started.Percent, started.StableVersion, started.CanaryVersion)
	fmt.Fprintf(b, "- 观测后：%s %d%%，再观测后：%s %d%%\n", mid.Kind, mid.Percent, done.Kind, done.Percent)
	fmt.Fprintf(b, "- 放量到 100%% 后，Acquire 读到候选配置：版本 %d，切分 %s，TopK %d\n\n", cfg.Version, cfg.ChunkStrategy, cfg.TopK)
	return nil
}

func runRollback(b *strings.Builder, history *evolution.History, stable, canary evolution.Result) error {
	mgr := config.NewManager()
	mgr.Register("kb-agent", config.Config{Name: "kb-agent", SystemPrompt: "stable-rollback", ChunkStrategy: "fixed", TopK: 3})
	rollout := evolution.NewRollout(mgr, "kb-agent")
	rollout.History = history
	started, err := rollout.Start(config.Config{Name: "kb-agent", SystemPrompt: "canary-rollback", ChunkStrategy: "recursive", TopK: 5})
	if err != nil {
		return err
	}
	if started.Kind != evolution.RolloutCanary || started.Percent != 10 || started.StableVersion != 1 || started.CanaryVersion != 2 {
		return fmt.Errorf("rollback start: %+v", started)
	}
	tenant, ok := config.TenantForBucket(0)
	if !ok {
		return fmt.Errorf("missing canary tenant")
	}
	held, err := mgr.AcquireTenant("kb-agent", tenant)
	if err != nil {
		return err
	}
	defer held.Release()
	before := held.Config()
	event, err := rollout.Observe(stable, canary)
	if err != nil {
		return err
	}
	if event.Kind != evolution.RolloutRollback || event.Percent != 0 || event.StableVersion != 1 || event.CanaryVersion != 2 {
		return fmt.Errorf("rollback event: %+v", event)
	}
	if held.Config().Version != before.Version || held.Config().SystemPrompt != before.SystemPrompt || held.Config().ChunkStrategy != before.ChunkStrategy || held.Config().TopK != before.TopK || held.Config().Version != 2 || held.Config().SystemPrompt != "canary-rollback" {
		return fmt.Errorf("in-flight config changed: %+v", held.Config())
	}
	next, err := mgr.Acquire("kb-agent")
	if err != nil {
		return err
	}
	defer next.Release()
	if next.Config().Version != 1 || next.Config().SystemPrompt != "stable-rollback" {
		return fmt.Errorf("new acquire after rollback: %+v", next.Config())
	}
	fmt.Fprintf(b, "## 灰度→回滚\n\n")
	fmt.Fprintf(b, "- 给定稳定窗口：样本 %d，来源命中 %.4f，词法代理 %.4f\n", stable.Cases, stable.HitRate, stable.Quality)
	fmt.Fprintf(b, "- 给定灰度窗口：样本 %d，来源命中 %.4f，词法代理 %.4f\n", canary.Cases, canary.HitRate, canary.Quality)
	fmt.Fprintf(b, "- 启动：%s %d%%（稳定版本 %d，灰度版本 %d）\n", started.Kind, started.Percent, started.StableVersion, started.CanaryVersion)
	fmt.Fprintf(b, "- 在途请求在回滚前拿到灰度配置，回滚后仍是版本 %d，提示 %s\n", held.Config().Version, held.Config().SystemPrompt)
	fmt.Fprintf(b, "- 观测后：%s %d%%。新请求回到稳定配置：版本 %d，提示 %s\n\n", event.Kind, event.Percent, next.Config().Version, next.Config().SystemPrompt)
	return nil
}
