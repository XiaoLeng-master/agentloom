# 03 · Benchmark

> 复现命令：`go run ./cmd/bench`（或 `bash scripts/bench.sh`）。

## 切分策略 × Top-K 的 HitRate@K 对比

数据集：`datasets/rag_eval_98.json`（98 条，公开来源）
语料：agentloom 自身公开文档（README / docs / 源码）——索引真实文件，不索引 groundTruth 本身，这样切分策略间才有区分度（把答案切到边界外的策略会 miss）。
Embedder：`HashEmbedder`（词法重叠基线，零依赖）。**不是语义 embedder**——这让策略间的 HitRate 差异归因到切分而非 embedder 噪声。
K = 3，命中 = 前 K 条结果含与 case 的 sourceDoc 同源的 chunk。

| 切分策略 | HitRate@K | MRR@K |
|---|---|---|
| fixed | 26.5% | 16.0% |
| recursive | 37.8% | 25.9% |
| semantic | 16.3% | 11.4% |
| parent-child | 19.4% | 9.4% |

**读法**：在本数据集 + 词法 embedder 下，recursive 胜出（37.8%），因为它按段落聚合 + overlap 保边界；fixed 把答案切到块外导致 miss；parent-child 因 dedupe 父文本略降召回。**不同数据/embedder 下最优策略不同**——这正是 benchmark 的价值：用数据选策略，不靠拍脑袋。

## 关于简历的「52%→68%」

简历妙画项目写的「HitRate@K 从 52% 提升至 68%」是**生产数据 + 真实 embedder（nomic-embed-text）**的口径。本仓库的 benchmark 用零依赖词法 embedder **复现的是方法论**（数据集 + 多策略对比 + 可复现脚本），不复现绝对值。要跑出生产级数字，把 `HashEmbedder` 换成 Ollama nomic-embed-text 即可，评测流程不变。

## 自进化闭环效果（前后对比）

每次优化的「预测 vs 实际」记录（预测式归因）：

| 轮次 | 优化项 | 预测 HitRate 变化 | 实际 | 预测匹配? |
|---|---|---|---|---|
| 1 | fixed→recursive | +10~15 | +11.3 | ✅ |
| 2 | recursive→semantic（换 embedder 后重测） | 待真 embedder | 待测 | — |
| 3 | 换 nomic-embed-text 后全策略重测 | 整体 +20~30 | 待测 | — |

> 不预置任何质量数字。上表第 1 轮来自本仓库实际跑出的结果（26.5%→37.8%），可一键复现；后续轮次待接入真实 embedder 后填入。
