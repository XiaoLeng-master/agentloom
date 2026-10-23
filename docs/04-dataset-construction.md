# 04 · 评估集构造方法

> 98 条标注评估集是怎么标的、为什么这么标、怎么扩展。

## 为什么自建评估集

私有 trace 不能开源（脱敏后失去结构），公开数据集（如 BEIR/MTEB）又不覆盖 Agent 评测关心的"切分/工具/记忆"维度。agentloom 自建一套**公开来源、可复现、可审计**的评估集——clone 下来就能跑，第三方能验证每一题。

## 数据 schema

`datasets/rag_eval_98.json`，每条 case 一个对象：

```json
{
  "id": "eval_001",
  "type": "retrieval",
  "question": "agentloom 的四层架构是哪四层？",
  "groundTruth": "控制面、执行面、记忆面、观测面",
  "sourceDoc": "docs/01-architecture.md"
}
```

| 字段 | 含义 | 约束 |
|---|---|---|
| `id` | 唯一标识 | `eval_001`..`eval_098`，连续不重复 |
| `type` | 评测维度 | retrieval / security / lifecycle / orchestration / infra / eval / attribution / closedloop / design |
| `question` | 用户提问 | 自然语言，像真实用户问的 |
| `groundTruth` | 标准答案 | **必须是仓库内代码或文档里能查到的事实**，不编造 |
| `sourceDoc` | 答案来源文件 | 指向 `docs/` 或 `internal/` 下真实存在的文件 |

## 标注规范（写新 case 的硬规则）

1. **可查证**：`groundTruth` 必须能在 `sourceDoc` 指向的文件里找到原文或直接推断。不接受"我觉得"的答案。
2. **不索引 groundTruth 本身**：benchmark 索引的是仓库文档/源码，**不**把 `groundTruth` 字段塞进语料——否则任何策略都能命中，失去区分度。
3. **覆盖能力而非覆盖文件**：case 的 `type` 要分散到 retrieval/security/lifecycle/orchestration/infra/eval 等多个维度，不能 80 条全是 retrieval。
4. **问题像用户**：用疑问句，不用关键词堆。好："限流分哪两级？" 坏："限流 全局 租户 两级"。

## 98 条的构成

| 段 | id 范围 | 来源 | 覆盖 |
|---|---|---|---|
| 原 80 条 | eval_001–080 | 仓库初版架构/设计文档 | retrieval / security 为主 |
| 新 18 条 | eval_081–098 | 新补的 plugin/orchestrator/mq 模块 + 已有架构补充 | lifecycle / orchestration / infra / eval / attribution / closedloop / design |

新 18 条对应代码模块，每条 groundTruth 都能在 `internal/runtime/plugin/plugin.go`、`internal/orchestrator/orchestrator.go`、`internal/infra/mq.go` 或 `docs/` 里查到。

## 怎么扩展（加新 case）

1. 在 `datasets/rag_eval_98.json` 末尾追加，`id` 递增。
2. 确保 `sourceDoc` 指向的文件**真实存在**（跑 `ls <sourceDoc>` 能过）。
3. 确保 `groundTruth` 在该文件里可查（`grep -l "groundTruth 关键词" <sourceDoc>` 能过）。
4. 跑 `go run ./cmd/bench` 确认数据集能加载、HitRate 合理（新 case 不应让某策略突涨到 90%+，否则说明 groundTruth 被索引了）。

## 为什么不公开 1000 条

98 条够区分策略（recursive 37.8% vs fixed 26.5%，差距 11 个点），且每条都可审计。1000 条需要标注成本，且边际收益递减——benchmark 的价值在**方法论可复现**，不在数量。生产环境用真实 trace 标注自己的私有集，方法论不变。
