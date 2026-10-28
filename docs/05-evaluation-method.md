# 05 · 评测方法

> 4 类评测器怎么算分、HitRate@K 怎么算、门禁怎么卡。

> **实现边界**：当前 `POST /v1/evolve` 会批量复测 98 条公开问题的来源命中，并将检索上下文送入 `internal/eval` 的规则评测器和确定性词法 Prober，记录 `deterministic_proxy` 质量、引用失败数和通过率后执行门禁。`POST /v1/evaluations` 可把真实请求 Trace 纳入 `runtime_evidence_rules` 批次，检查请求、回答、检索依据和 Contract 链是否完整；当请求问题与公开评估集精确匹配时，再评估该次实际回答。未设置 DeepSeek Key 时使用 `deterministic_actual_answer`；设置后使用 `deepseek_geval_actual_answer`，读取评分 token 的 logprobs 计算加权分。没有匹配 ground truth 的请求不会生成答案质量分。当前仍没有 claim-level faithfulness 或安全评测，真实 Judge 的线上方差也需实测验收。

## 评测器（internal/eval/）

agentloom 实现 4 类评测器，统一实现 `Evaluator` 接口：

```go
type Evaluator interface {
    Name() string
    Eval(Trace) Score
}
```

| 评测器 | 评什么 | 怎么算 | 代码 |
|---|---|---|---|
| **G-Eval** | 答案质量分 | 见下"G-Eval 算法" | `eval/geval.go` |
| **RAG 来源召回代理** | 引用是否覆盖期望来源 | 去重后的引用来源召回；不判断答案声明真实性 | `eval/evaluators.go` |
| **检索排序** | 检索结果排序质量 | HitRate@K + MRR@K（见下） | `cmd/bench/main.go` |
| **任务完成** | Agent 是否完成用户意图 | 规则匹配终态 + 工具调用轨迹校验 | `eval/evaluators.go` |

### G-Eval 算法（logprobs 概率归一化）

直接让 LLM 打 1-5 分会**离散波动**（同一答案跑两次可能 3 分和 4 分）。G-Eval 用 logprobs 把离散分变成连续概率：

1. 让 Judge 模型对答案输出评分 Token（如 "1"/"2"/"3"/"4"/"5"）。
2. 取每个评分 Token 的 logprob（模型对该 Token 的对数概率）。
3. 归一化：`p_i = exp(logprob_i) / Σ exp(logprob_j)`（softmax over 候选 Token）。
4. 加权：`score = Σ (rating_i * p_i)`（如 p(5)=0.6, p(4)=0.3, p(3)=0.1 → 4.5 分）。

这样即使模型最可能输出 "4"，只要 p(5) 不为 0，分数就略高于 4，反映模型对 "5" 的部分置信。**单次跑分稳定**，不用多次平均。

代码：`internal/eval/geval.go` 和 `internal/eval/deepseek_judge.go`。默认 Prober 依据词法重叠生成测试概率；设置 `DEEPSEEK_API_KEY` 后，真实请求 Trace 的答案评测使用 DeepSeek Chat Completions 的 `logprobs` 与 `top_logprobs`。外部模型不可用、响应缺少评分 token 或分布无效时失败关闭，不用词法分悄悄替代。

### HitRate@K / MRR@K（检索质量）

```
HitRate@K = 命中数 / 总 case 数
命中 = 前 K 条检索结果含与 case.sourceDoc 同源的 chunk

MRR@K = Σ (1 / 首次命中排名) / 总 case 数
```

K=3（前 3 条结果里有没有同源 chunk）。`make bench` 一键跑。

## 闭环：评测怎么驱动优化

```
Trace 采集（mq 削峰 + 按月分表）
    ↓
回归集批量评测（4 评测器并发）
    ↓
失败聚类归因（规则优先，LLM-as-Judge 兜底）
    ↓ 归因到层
优化任务生成（检索层 / Prompt 层 / 工具层）
    ↓
复测对比 → 门禁
    ↓ 门禁通过
热更新灰度上线（config.Manager 引用计数零中断）
```

### 门禁规则

- **HitRate@K 不降**：新版本 HitRate ≥ 旧版本（允许持平，不允许下降）。
- **G-Eval 均分不降**：同口径对比，分数降则拒绝上线。
- **无新增 Faithfulness 失败**：新版本不能引入新的幻觉声明。

三条都过才灰度。任一不过，优化任务标 failed，回流给归因引擎重判。

上述是目标门禁。当前接口已检查同一数据快照下的检索命中、确定性质量代理不降和新增引用失败为零；门禁通过后如果实验证据无法保存，会恢复先前配置并返回回退版本。它仍不执行真实模型 G-Eval、claim-level Faithfulness，也没有基于线上监控信号的自动回滚。因此门禁通过代表“可复现离线代理实验”通过，不能代表生产答案质量通过。

## 怎么跑

```bash
# 检索质量 benchmark（HitRate@K 多策略对比）
make bench
# 或: go run ./cmd/bench

# 评测器单测
go test -race ./internal/eval/

# 真实请求 Trace 的运行证据批次（1–100 个 trace_id，需管理员令牌）
curl -X POST -H 'Authorization: Bearer local-demo' -H 'Content-Type: application/json' \
  -d '{"trace_ids":["<trace_id>"]}' localhost:8080/v1/evaluations

# 使用管理员令牌读回批次列表或单个批次
curl -H 'Authorization: Bearer local-demo' localhost:8080/v1/evaluations
curl -H 'Authorization: Bearer local-demo' localhost:8080/v1/evaluations/<batch_id>
```

输出示例（`make bench`）：

```
| 切分策略 | HitRate@K | MRR@K |
|---|---|---|
| fixed | 以当前 `make bench` 输出为准 | 同左 |
| recursive | 以当前 `make bench` 输出为准 | 同左 |
```

## 设计取舍：为什么规则优先、LLM-as-Judge 兜底

规则可复现可解释（HitRate 是确定的数学公式）；Judge 有随机性。闭环里规则先跑，**只在规则无法归因时**（如"答案语义偏题但检索命中"）才调 Judge 补尾巴。这样评测器本身不会成为新的噪声源。详见 `docs/02-design-decisions.md` 第 2 条。
