# 08 · 并发配额

本文记录两件事：`/v1/chat` 的在途准入怎么和 `2291d87` 的 Chat 槽位、按来源限流收成一套；以及在 commit `0945d8dc17a67192a30ae9e1eb4378dca4050af7` 上重跑的 Executor 压测。准入和拒绝会随调度波动，下面写的是 16 次运行的区间，不是单次拆解。

测量时工作区干净，只含该 commit。分支已用 merge（不是 rebase）纳入 `origin/main` 的 `2291d87`（完善公开演示权限与确定性评测闭环）。并发配额文档用 08，因为 07 已是公开演示说明。

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

`cmd/quotabench` 使用进程内 stub，不访问网络，也不调用模型 API。每个 `Run` 有 4 个 Executor 子任务。默认参数：worker 4，全局并发 3，每个 Executor 2，睡眠 20ms，12 个并发 `Run`，其中 4 个超时 30ms、其余 5s。12 × 4 = 48 次调用尝试。Executor 跑在该回合的 worker 里：panic 转成这个子任务的错误；第一个执行错误会取消其余子任务。本地 `/v1/chat` 的演示 Planner 只产出 1 个子任务，并发上限要看这里的压测，而不是演示对话。`SetConcurrency` 在有回合运行时会返回错误，避免新旧两套信号量把全局上限放大。

在仓库根目录、Go 1.22.2（`go1.22.2 linux/amd64`）先编译再各跑 8 次，共 16 次，时间从 `2026-10-27T08:42:23.843614479Z` 到 `2026-10-27T08:42:34.778026443Z`。观测并发若超过配置上限，进程会以退出码 1 结束；16 次都是退出码 0。

```bash
go build -o /tmp/qb ./cmd/quotabench
go build -race -o /tmp/qb-race ./cmd/quotabench
# 普通二进制与 race 二进制各执行 8 次
```

| 指标 | 16 次结果 |
|---|---|
| admitted | 32–38 |
| rejected | 10–16 |
| admitted + rejected | 48（16/16） |
| observed_peak_inflight | 3（16/16） |
| exec-a / exec-b peak | 2 / 2（16/16） |
| quota_peak_inflight | 3（16/16） |
| quota_peak_queued | 45（16/16） |
| combined_peak_queued | 45（16/16） |
| runs_ok / runs_failed | 8 / 4（16/16） |
| inflight_end / queued_end | 0 / 0（16/16） |
| wall_ms | 223–256 |

- 观测到的 Executor 峰值并发是 **3**，`exec-a` / `exec-b` 各 **2**，都等于本次配置的上限。
- 合计排队峰值与信号量排队高水位在这 16 次里都是 **45**。合计值在同一把锁里用「pool 积压 + 信号量排队」更新，移交时先离开积压再进入信号量，所以同一次尝试不会被加两次。45 没有超过 48 次尝试。每个 `Run` 的 worker 数不少于子任务数，排队主要在信号量上；全局上限是 3，因此最多有 3 个在执行，其余才算排队。
- 准入 **32–38**，拒绝 **10–16**，两者之和恒为 **48**。成功/失败次数每次都是 8/4。短超时那 4 个 `Run` 上的准入次数会变，不能把某一次的「成功 32 + 短超时若干」写成固定拆分。
- 结束时在途和排队都是 0。墙钟 **223–256ms**。

同一次提交上，紧挨着这组压测之前，还跑了：

```bash
go test -count=1 ./...
go test -count=1 -race ./...
go vet ./...
```

三个命令退出码都是 0。`cmd/bench`、`cmd/evolve`、`cmd/quotabench` 没有测试文件。其余包 `go test -count=1 -race ./...` 均为 `ok`：`cmd/agentd`、`internal/app`、`internal/config`、`internal/core`、`internal/eval`、`internal/evolution`、`internal/gateway`、`internal/infra`、`internal/mcp`、`internal/orchestrator`、`internal/quota`、`internal/rag`、`internal/runtime/plugin`、`web`。

## 边界

滑动窗口仍是单进程、按来源地址的每分钟计数，超额立刻拒绝，不排队。在途准入也在单进程内存里。`docs/01-architecture.md` 里的两级令牌桶已标成目标架构。这次入口落地的是并发槽位，加上原有的滑动窗口。关停时 `http.Server.BaseContext` 会被取消，排在信号量里的 Chat 不会干等满 5 秒。Limiter 本身仍没有排队长度上限，超额请求会一直排到 context 结束。
