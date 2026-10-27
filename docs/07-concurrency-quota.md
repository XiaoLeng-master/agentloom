# 07 · Executor 并发配额测量

本文只记录 **一次** 在本仓库里实际跑出来的结果。这不是目标值，也不是 SLA。再跑一次，排队高水位、拒绝次数和墙钟时间都会变。下文数字来自这次进程的标准输出，没有事后改过。

测量时工作区干净，HEAD 为 `2b88621d89ebdf8a747e26027af8bf1b73236f4c`（`修正配额准入计数，避免超时撞车被重复计入`，2026-10-27 08:21:07 UTC）。记录本文的提交在它之后，不包含在这次测量的代码里。

## 测什么

`cmd/quotabench` 拉起一个 `orchestrator.Orchestrator`，Planner / Executor / Verifier 都是进程内 stub，不访问网络，也不调用任何模型 API。Executor 的 `Complete` 睡眠 20ms，用来制造重叠。

每个 `Run` 有 4 个子任务，在该回合自己的 worker pool 里并发执行。所有回合共享同一套两级信号量：

- 全局同时执行的 Executor 调用不超过 `-global`
- 同一个 Executor Agent 名称不超过 `-per-agent`
- 拿不到槽位就排队；`context` 超时或取消时记一次拒绝，不执行该子任务

Verifier 仍按子任务顺序串行执行，fail-closed 和 replan 的判定顺序与改动前一致。

默认参数（本次没有额外传 flag）：

| 参数 | 值 |
|---|---|
| workers | 4 |
| global | 3 |
| per-agent | 2 |
| requests | 12（并发 `Run`） |
| tasks | 4（每个 `Run` 的子任务） |
| exec-sleep | 20ms |
| short-timeout-requests | 4（使用 30ms 超时） |
| 其余请求超时 | 5s |
| Agent | `exec-a`、`exec-b` 两个 Executor |

12 × 4 = 48 次 Executor 调用尝试。

## 命令

在仓库根目录、Go 1.22.2（`go1.22.2 linux/amd64`）下执行：

```bash
go run -race ./cmd/quotabench
```

开始时间 `2026-10-27T08:21:10.690437475Z`，结束时间 `2026-10-27T08:21:10.927259674Z`。进程退出码 0。程序会在观测到的并发超过全局上限或按 Agent 上限时以退出码 1 结束；这次没有触发。

## 这次的输出

```json
{
  "admitted": 36,
  "combined_peak_queued": 45,
  "exec_sleep_ms": 20,
  "finished_at": "2026-10-27T08:21:10.927259674Z",
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
  "rejected": 12,
  "requests": 12,
  "runs_failed": 4,
  "runs_ok": 8,
  "short_timeout_ms": 30,
  "short_timeout_requests": 4,
  "started_at": "2026-10-27T08:21:10.690437475Z",
  "subtasks_per_request": 4,
  "wall_ms": 236,
  "workers": 4
}
```

## 这些字段

- `observed_peak_inflight` 是 Executor stub 里同时处于 `Complete` 的最大个数，这次是 **3**，等于 `global_limit`。
- `observed_per_agent_peak` 是每个 Executor 名称自己的最大重叠，`exec-a` **2**、`exec-b` **2**，等于 `per_agent_limit`。
- `quota_peak_inflight` 是信号量自己记下的在途高水位，这次也是 **3**。
- `quota_peak_queued` 是卡在信号量等待队列里的高水位，这次是 **45**。
- `combined_peak_queued` 是「还没被 worker 取走的积压 + 信号量等待」的高水位，这次也是 **45**。每个 `Run` 的 worker 数（4）不少于子任务数（4），任务很快离开 pool 积压，这次排队主要堆在两级信号量上。
- `admitted` **36**：`Acquire` 成功并返回 release 的次数。
- `rejected` **12**：还在排队时 `context` 结束、因而没有执行的次数。
- `36 + 12 = 48`，等于 12 个请求 × 4 个子任务。成功的 8 个请求各准入 4 次，共 32 次；剩下的 4 次准入和 12 次拒绝落在 4 个 30ms 超时请求上（4 + 12 = 16）。
- `runs_ok` **8**，`runs_failed` **4**，与「4 个短超时、8 个 5s 超时」的设置一致。
- 结束时 `inflight_end` 和 `queued_end` 都是 **0**。墙钟 **236ms**。

同一次提交上另有 `go test -count=1 -race ./...`（2026-10-27T08:21:10Z 起跑，退出码 0）。`cmd/bench`、`cmd/evolve`、`cmd/quotabench` 没有测试文件；其余包均为 `ok`：`cmd/agentd`、`internal/app`、`internal/config`、`internal/core`、`internal/eval`、`internal/evolution`、`internal/gateway`、`internal/infra`、`internal/mcp`、`internal/orchestrator`、`internal/quota`、`internal/rag`、`internal/runtime/plugin`、`web`。

和配额直接相关的测试：`TestWorkerPoolCapsInflight`、`TestGlobalQuotaCapsInflight`、`TestPerAgentQuota`、`TestQuotaTimeoutRejects`、`TestResultOrderStable`，以及 `internal/quota` 和 `internal/gateway` 里的信号量单测。

## 实现边界

演示进程的 `/v1/chat` 会走到 `Orchestrator.Run`，因此会用到默认 worker pool（4）和默认两级配额（全局 8、每个 Executor 名称 4）。网关链上的 `RateLimitFilter` 仍是单机、按租户的滑动窗口，超额立即拒绝，不排队。`ConcurrencyAdmission`（全局 + 按租户，没有租户时按 Agent 名称）有单测，但没有接进演示用的 Filter Chain。

配额是进程内信号量，不是跨实例的 Redis 或 etcd 计数。`docs/01-architecture.md` 里写的「全局/租户两级限流（令牌桶）」仍是目标架构里的句子；这次落地的是并发槽位，不是令牌桶速率。
