# 08 · 并发配额

本文记录两件事：`/v1/chat` 的在途准入怎么和 `2291d87` 的 Chat 槽位、按来源限流收成一套；以及在合并之后的 commit `62ce1d41b367b8bd52bea3bd9305ae68a04a4001` 上重新跑出的 Executor 压测。压测数字是该次进程的原始输出。再跑一次，排队高水位和拒绝次数会变。

测量时工作区干净。记录本文的提交在 `62ce1d4` 之后。分支已用 merge（不是 rebase）纳入 `origin/main` 的 `2291d87`（完善公开演示权限与确定性评测闭环）。

## `/v1/chat` 只有一道在途门

`2291d87` 用一个容量为 4 的 channel 立刻拒绝第 5 个在途 Chat，另外用滑动窗口按来源地址每分钟 60 次。本分支原先还有一套未接到 Chat 的 `ConcurrencyAdmission`。两套在途上限不再并存。

现在的顺序：

1. 公开模式权限（`AccessPolicy`）。未授权的请求不占槽。本地模式不设置 `AGENTLOOM_PUBLIC_MODE` 时，Chat 仍可直接访问；`POST /v1/evolve` 仍要本地管理员令牌 `local-demo`。
2. 注入检测，以及按来源地址的滑动窗口（默认 60 次/分钟）。来源是 `RemoteAddr` 的 host。这是请求速率，不是在途槽位。
3. 唯一的在途准入：全局信号量默认 4，同一来源的信号量默认也是 4。超额的请求排队，直到有空位或请求 `context` 取消/超时。超时返回 HTTP 429，并计入拒绝数。

`AGENTLOOM_CHAT_GLOBAL` 与 `AGENTLOOM_CHAT_PER_SOURCE` 可覆盖默认 4。公开模式不再另外做一次 `WithMaxConcurrentChats(4)`。

入口放行之后，一个回合内部的 Executor 子任务仍走 worker pool，以及另一套全局 + 按 Agent 名称的信号量。那是放大后的下游并发，和入口这 4 个槽位不是同一个计数器。

HTTP 行为由 `TestChatAdmissionQueuesAndRejects`、`TestChatAdmissionPerSource`、`TestPublicTokenDenialDoesNotTakeChatSlot` 覆盖。下面的 `quotabench` 不打 `/v1/chat`，不能当作入口槽位的压测。

## Executor 压测

`cmd/quotabench` 使用进程内 stub，不访问网络，也不调用模型 API。每个 `Run` 有 4 个 Executor 子任务。默认参数：worker 4，全局并发 3，每个 Executor 2，睡眠 20ms，12 个并发 `Run`，其中 4 个超时 30ms、其余 5s。12 × 4 = 48 次调用尝试。

在仓库根目录、Go 1.22.2（`go1.22.2 linux/amd64`）执行：

```bash
go run -race ./cmd/quotabench
```

开始 `2026-10-27T08:30:05.421636004Z`，结束 `2026-10-27T08:30:05.647559537Z`，退出码 0。观测并发若超过配置上限，进程会以退出码 1 结束；这次没有。

```json
{
  "admitted": 33,
  "combined_peak_queued": 45,
  "exec_sleep_ms": 20,
  "finished_at": "2026-10-27T08:30:05.647559537Z",
  "global_limit": 3,
  "go_version": "go1.22.2",
  "inflight_end": 0,
  "long_timeout_ms": 5000,
  "observed_peak_inflight": 3,
  "observed_per_agent_peak": {
    "exec-a": 2,
    "exec-b": 2
  },
  "per_agent_limit": 2,
  "queued_end": 0,
  "quota_peak_inflight": 3,
  "quota_peak_queued": 45,
  "rejected": 15,
  "requests": 12,
  "runs_failed": 4,
  "runs_ok": 8,
  "short_timeout_ms": 30,
  "short_timeout_requests": 4,
  "started_at": "2026-10-27T08:30:05.421636004Z",
  "subtasks_per_request": 4,
  "wall_ms": 225,
  "workers": 4
}
```

- 观测到的 Executor 峰值并发是 **3**，`exec-a` / `exec-b` 各 **2**，都等于本次配置的上限。
- 信号量排队高水位 **45**，与 pool 积压加信号量排队的高水位相同。每个 `Run` 的 worker 数不少于子任务数，排队主要在信号量上。
- 准入 **33**，拒绝 **15**，`33 + 15 = 48`。成功的 8 个 `Run` 各准入 4 次，共 32 次；剩下 1 次准入和 15 次拒绝落在 4 个 30ms 超时的 `Run` 上（1 + 15 = 16）。
- 结束时在途和排队都是 0。墙钟 **225ms**。

同一次提交上，紧挨着这次压测之前，还跑了：

```bash
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

三个命令退出码都是 0。`cmd/bench`、`cmd/evolve`、`cmd/quotabench` 没有测试文件。其余包 `go test -count=1 -race ./...` 均为 `ok`：`cmd/agentd`、`internal/app`、`internal/config`、`internal/core`、`internal/eval`、`internal/evolution`、`internal/gateway`、`internal/infra`、`internal/mcp`、`internal/orchestrator`、`internal/quota`、`internal/rag`、`internal/runtime/plugin`、`web`。

## 边界

滑动窗口仍是单进程、按来源地址的每分钟计数，超额立刻拒绝，不排队。在途准入也在单进程内存里。`docs/01-architecture.md` 里的两级令牌桶仍是目标描述。这次入口落地的是并发槽位，加上原有的滑动窗口。
