# 真实 DeepSeek 端到端记录（2026-10-27）

这是一次小规模的人工端到端运行，不是规模化评测。只写实测数；失败和不足照实列出。

## 运行信息

| 项 | 值 |
|---|---|
| 运行时间 | 2026-10-27 17:17:53 启动 agentd，17:18:06–17:18:29 完成全部模型调用（北京时间，UTC+8） |
| 代码 | 分支 `feature/p0-wording-fixes`，基于提交 `8f4e10d3f7a20739ea310c27024c29f62192e600`，加上本次提交里的调用计量代码（`internal/core/deepseek.go`、`cmd/agentd/main.go`）。运行时 README/docs 尚未改动 |
| 运行环境 | 本地 Linux 机器，Go 1.27.3，`agentd` 监听 `127.0.0.1:18080`，单进程 |
| 模式 | 公开模式：`AGENTLOOM_PUBLIC_MODE=1`，管理员令牌为本次随机生成的一次性值，未设访客令牌 |
| 模型 | `deepseek-flash`（请求和响应里的 `model` 字段都是 `deepseek-flash`），DeepSeek 官方 Chat Completions 接口 |
| 参数 | `max_tokens=1200`（代码默认），`thinking` 关闭，`stream=false`，Executor 带 `search_docs` 工具，工具返回后 `tool_choice=none` |
| 只有 Executor 用真实模型 | Planner 是单任务规则，Verifier 是确定性非空规则（不验证事实正确性） |
| 检索 | HashEmbedder，K=3，索引 README.md、docs/01-architecture.md、docs/02-design-decisions.md；整次运行切分策略都是 fixed |
| 调用上限 | `AGENTLOOM_DEEPSEEK_MAX_CALLS=20`（进程内硬上限），任务预算 30 次 |
| **真实 DeepSeek 调用次数** | **8 次**（4 个 Chat 请求 × 每个 2 次：一次产生工具调用，一次生成回答），全部 HTTP 200 |
| token 合计（接口返回的 usage） | prompt 2149，completion 989，total 3138 |

## 结果表

延迟是客户端测得的整次 `/v1/chat` 往返时间；token 是该请求 2 次模型调用的 usage 之和（prompt/completion）。

| # | 问题 | 预期 | HTTP | 延迟 | token（prompt/completion/total） | 检索来源（分数） | 结果 |
|---|---|---|---|---|---|---|---|
| q1 | agentloom 的 Planner、Executor、Verifier 三个角色分别负责什么？ | 应命中 docs/01 的角色段落 | 200 | 3879 ms | 521 / 264 / 785 | README.md (0.408)、README.md (0.289)、docs/01-architecture.md (0.246) | **未命中目标内容**：fixed 切分只返回了 README 链接片段和 docs/01 的网关链片段。模型说明“没找到明确定义角色职责的文档，无法回答”，没有编造 |
| q2 | agentloom 的配置热更新为什么用 Watch 加引用计数，而不是只用 Watch？ | 应命中 docs/02 第 1 节 | 200 | 4123 ms | 553 / 353 / 906 | docs/02-design-decisions.md (0.378)、docs/01-architecture.md (0.354)、docs/02-design-decisions.md (0.250) | **命中**：回答“纯 Watch 会让旧实例立刻失效、打断在跑请求”，引用 docs/02 第 1 节和 docs/01 ⑦；并说明引用计数实现细节在检索片段里没有 |
| q3 | agentloom 在 2025 年双十一当天处理的峰值 QPS 是多少？请给出具体数字。 | 应拒答 | 200 | 3200 ms | 555 / 261 / 816 | README.md (0.447)、README.md (0.365)、README.md (0.258) | **拒答正确**：“搜索结果里没有找到相关数字，无法回答”，没有给出数字 |
| q4 | 同 q1（在 evolve 之后重问） | 若 evolve 通过应切到 recursive | 200 | 2763 ms | 520 / 111 / 631 | 与 q1 相同 | evolve 门禁未通过（见下），仍是 fixed，检索结果与 q1 相同；模型再次说明证据不足 |

逐次调用（来自 agentd 日志，日志不含请求头和 Key）：

| n | 所属请求 | 延迟 | prompt | completion | total |
|---|---|---|---|---|---|
| 1 | q1 | 1768 ms | 317 | 60 | 377 |
| 2 | q1 | 2100 ms | 204 | 204 | 408 |
| 3 | q2 | 1062 ms | 319 | 55 | 374 |
| 4 | q2 | 3051 ms | 234 | 298 | 532 |
| 5 | q3 | 1073 ms | 324 | 57 | 381 |
| 6 | q3 | 2118 ms | 231 | 204 | 435 |
| 7 | q4 | 1398 ms | 317 | 59 | 376 |
| 8 | q4 | 1355 ms | 203 | 52 | 255 |

4 个请求的响应都是 `mode=deepseek`、`verification=passed`；Trace 都有 plan → execute → verify 三段契约和 3 条检索证据。

## 评测路径

- 代码里没有需要 LLM 判分的评测路径：`POST /v1/evolve`、`GET /v1/bench`、`cmd/bench`、`cmd/evolve` 都只用来源命中和确定性词法代理，不调用模型；Verifier 也是确定性规则。因此本次**没有跑 LLM 判分**，真实回答质量仍未评。
- 运行中调用了一次 `POST /v1/evolve`（管理员令牌，0 次模型调用）：HTTP 409，`regression gate failed`。来源命中 fixed 0.1939 → recursive 0.3367（+0.1429，落在预测区间 0.10–0.25 内），但确定性代理分数 0.4427 → 0.4380 下降、通过率 1.0% → 0%，门禁不放行，配置没有切换，所以 q4 仍用 fixed。这和 README 表里的数字一致，不是本次运行引入的问题。

## 复现步骤

```bash
git clone -b feature/p0-wording-fixes https://github.com/XiaoLeng-master/agentloom && cd agentloom
# DEEPSEEK_API_KEY 只从本机环境变量读取，不要写进任何文件或命令行参数
export AGENTLOOM_PUBLIC_MODE=1
export AGENTLOOM_ADMIN_TOKEN="$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
export DEEPSEEK_MODEL=deepseek-flash
export AGENTLOOM_DEEPSEEK_MAX_CALLS=20   # 进程内调用上限，超过后直接拒绝
go run ./cmd/agentd -addr 127.0.0.1:18080 2> agentd.log &
curl -s -H 'Content-Type: application/json' \
  -d '{"message":"agentloom 的配置热更新为什么用 Watch 加引用计数，而不是只用 Watch？"}' \
  127.0.0.1:18080/v1/chat
grep 'deepseek call' agentd.log   # 每次调用一行：模型、状态码、延迟、token
```

每个 Chat 请求在当前代码下最多 4 次模型调用（Executor 最多 2 轮 × 最多 1 次重规划），正常是 2 次。

## 局限

- 规模很小：4 个请求、8 次调用、1 次运行。不能据此给出准确率、延迟分位数或成本结论；模型输出有随机性，重跑结果会不同。
- 只有 Executor 用了真实模型；Planner、Verifier 是规则，Verifier 不检查事实正确性。
- 检索是 HashEmbedder + fixed 切分，q1 这种应命中的问题实际没检索到目标段落；这暴露的是检索问题，不是模型问题。
- 没有跑任何 LLM 判分；“回答是否正确”只是人工对照文档看了这 4 条。
- 接口返回的 prompt_tokens 在第二次调用（带工具结果）时反而比第一次小，原样记录；推测与上下文缓存计数有关，未核实。
- 运行用的二进制是 `8f4e10d` 加未提交的计量代码；README 和 docs 的口径改动在运行之后。README 在评估集里，这次改动后重跑了 `go run ./cmd/bench` 并更新了表格。
