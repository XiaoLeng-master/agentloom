# 05 · 评测方法

> 4 类评测器怎么算分、HitRate@K 怎么算、门禁怎么卡。

> **实现边界**：`make loop` 与 `POST /v1/evolve` 在固定 98 条上做离线复测。来源命中看期望文档是否出现在 Top-K。`deterministic_proxy` 把首个检索片段当作代理答案，用词法 Prober 相对 groundTruth 打分，并统计引用是否覆盖期望来源。渲染回答里的事实覆盖只用于归因，不用于晋升。三项都不是模型生成的答案质量，也不是 LLM Judge 或 claim-level faithfulness。`GET /v1/bench` 仍是只读的 fixed/recursive 来源命中预览，`answer_quality` 为 `not_evaluated`。门禁通过后候选进入进程内灰度，观测回落会自动回滚，稳定则逐步放到 100%。`make canary` 复现的是命令给定窗口，不是这条 98 条链路的在线灰度。etcd 灰度源仍未实现。具体数字以该提交的 `make loop` 输出为准。

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

代码：`internal/eval/geval.go`。当前默认 Prober 依据词法重叠生成测试概率，不调用真实 Judge；接入模型需另行实现并验证 logprobs 口径。

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

上述是目标门禁。当前闭环检查同一数据快照下的来源命中不降、`deterministic_proxy` 词法分不降（容差 1e-6）、不新增引用失败、归因失败数不增加，并且至少一项硬指标变好。它不执行真实模型 G-Eval、claim-level Faithfulness 或安全评测。进程内灰度按租户哈希分流，观测窗口回落会回滚，稳定则放到 100%；etcd 灰度源仍未实现。`make canary` 的窗口是命令给定的，不是在线测量。通过只表示离线硬指标没有退步且有一项变好，不能表示生成答案变正确。连续调用 `POST /v1/evolve` 时，基线取当前生效的切分、Prompt 渲染模式和 TopK。

## 怎么跑

```bash
# 检索质量 benchmark（HitRate@K 多策略对比）
make bench
# 或: go run ./cmd/bench

# 评测器单测
go test -race ./internal/eval/

# 评测—归因—优化—复测门禁（在仓库根目录执行）
make loop
# 或: go run ./cmd/loop
# JSON：go run ./cmd/loop -json
```

`make loop` 与 `POST /v1/evolve` 绑定 `datasets/rag_eval_98.json`。两者都用确定性本地替身（不调用外部 LLM）对每条样本检索并渲染回答，不经过 Chat 运行时。失败按模拟调用记录归因：期望来源未命中，或来源命中但检索上下文没有 ground truth，归检索层；tool error / unknown tool / max iterations 归工具层；只有上下文含有 ground truth 而渲染回答没有时归 Prompt 层。优化任务写成结构化记录（层、原因、候选配置、预测区间）。预测区间是失败占比乘固定系数，不是校准后的置信区间。公开基线上工具策略一直是 search，工具层失败数为 0，那三类信号由单元测试覆盖。

当前提交实测（`go run ./cmd/loop`，语料哈希 `65135211c07420203ee0da76f4762432c9ae196bd258a477a4398384471d3ebb`）：基线 fixed / echo / top-3，来源命中 0.1939，词法代理 0.4402，引用失败 79，归因失败 98（来源未命中 79，上下文缺少 ground truth 19，Prompt 0，工具 0）。recursive 来源命中 +0.1531，预测命中，但词法代理下降，门禁拒绝。semantic 来源命中 −0.0204，预测未命中，门禁拒绝。parent-child 来源命中 −0.0204，预测未命中，门禁拒绝。retrieval-topk 把 TopK 提到 5，来源命中 +0.1122（到 0.3061），预测命中，词法代理与基线相同，引用失败 79→68，归因失败数仍是 98，门禁通过并进入 10% 灰度。灰度配置版本是 2，稳定配置仍是版本 1。这不是生成答案变正确，也没有减少归因失败。

输出示例（`make bench`）：

```
| 切分策略 | HitRate@K | MRR@K |
|---|---|---|
| fixed | 以当前 `make bench` 输出为准 | 同左 |
| recursive | 以当前 `make bench` 输出为准 | 同左 |
```

## 设计取舍：为什么规则优先、LLM-as-Judge 兜底

规则可复现可解释（HitRate 是确定的数学公式）；Judge 有随机性。闭环里规则先跑，**只在规则无法归因时**（如"答案语义偏题但检索命中"）才调 Judge 补尾巴。这样评测器本身不会成为新的噪声源。详见 `docs/02-design-decisions.md` 第 2 条。
