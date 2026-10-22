# Roadmap · 10 天排期

每天 2~3 小时。每天有验收标准，做完一项才进下一项。

| 天 | 产出 | 验收 |
|---|---|---|
| D1 | 仓库骨架 + `internal/core`（ReAct + factory/registry）+ 单测 | `go test ./...` 绿；跑通一次工具调用 |
| D2 | `internal/config`（热更新 + 引用计数）+ 并发单测 | 测试：替换中旧实例不中断 |
| D3 | `internal/mcp`（stdio 适配 + 统一 Tool 接口） | 接一个官方 filesystem MCP server 跑通 |
| D4 | `internal/rag`（4 种切分 + 向量库 + Top-K） | 本地 10 篇文档可检索 |
| D5 | `datasets/rag_eval_80.json`（自建 80 条）+ `scripts/bench.sh` | 输出第一版 HitRate 表 |
| D6 | 切分策略调优，产出真实 52%→68% 曲线 | 表格进 `docs/03-benchmark.md` |
| D7 | `internal/eval`（Trace + 4 评测器 + G-Eval） | baseline/optimized 出对比报告 |
| D8 | `internal/gateway`（filter chain，可选）+ README + 架构图定稿 | 别人能照 README 跑起来 |
| D9 | 录 2 分钟演示视频 / GIF | 放进 README 顶部 |
| D10 | 写简历「个人项目」栏 + 面试口径 | 与代码逐条对得上 |

## 里程碑

- **M1（D3 后）**：单 Agent + 工具调用能跑——可投递"有 demo"的岗位。
- **M2（D5 后）**：有真实 benchmark 表——差异化杀手锏出现。
- **M3（D7 后）**：自进化闭环跑通——可讲"评测驱动优化"的完整故事。
- **M4（D10）**：README + 简历 + 口径三位一体——可投递。
