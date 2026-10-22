<div align="center">

# agentloom

**A multi-agent runtime that learns from its own traces.**

把 ReAct 循环、MCP 工具、RAG 检索、配置热更新和多 Agent 分层协作做在同一套 Go 平台里，并带一个评测 harness——让 Agent 能基于自己的执行轨迹持续优化检索、Prompt 与工具选择，而不只是跑通就结束的 demo。

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Status](https://img.shields.io/badge/status-WIP%20%7C%20skeleton-orange.svg)](#status)

</div>

> **适合谁**：想在自己的知识库 / 工单 / 问答场景里跑一个可配置、可观测、可自进化 Agent 的后端工程师。Go 单二进制，零外部依赖即可跑起来。

---

## 为什么造这个

市面上的 Agent 项目大多停在"单 Agent + 工具调用 + RAG"的 demo 层。生产里真正难的三个问题很少有人把代码摆出来：

1. **请求放大效应**：用户发 1 条消息，Agent 要跑 3 次 LLM + 2 次工具 + 2 次检索。表面并发 1000，实际是 5000 次 LLM 调用 + 上万次存储访问。**后端并发 / 缓存 / MQ 在 Agent 系统里不是装饰，是刚需。**
2. **没有自进化闭环**：Agent 上线即定稿，失败案例不回流、不归因、不优化，永远在犯同一个错。
3. **多 Agent 不是"塞进一个聊天框"**：而是按能力拆池——贵模型做规划（少而精）、平价模型做执行（横向扩）、规则 + 小模型做校验。

agentloom 把这三件事做在同一套平台里，并且**让每一环都咬合**：配置热更新是自进化闭环的发布通道，网关链是多 Agent 协作的总线，RAG 评测是闭环的第一个优化对象。

## 架构

<p align="center">
  <img src="docs/architecture.svg" alt="agentloom architecture" width="960">
</p>

详见 [`docs/01-architecture.md`](docs/01-architecture.md)。

## 核心特性

| 特性 | 说明 | 状态 |
|---|---|---|
| **多 Agent 分层协作** | Planner / Executor / Verifier 能力池分离，按成本与负载独立扩缩 | 🚧 |
| **Harness 自进化闭环** | Trace 采集 → 批量评测 → 失败聚类归因 → 优化任务 → 复测门禁 → 热更新灰度 | 🚧 |
| **预测式归因** | 每次优化提交声明预期收益，评测验证预测成立与否，逐次变更可归因 | 📋 |
| **配置热更新** | 引用计数安全替换，运行中秒级切换、零中断（Source 接口，etcd 为生产 Source） | ✅ |
| **MCP 双传输适配** | stdio / HTTP 双传输 → 统一 Tool 契约，schema 校验 + 超时重试 + 错误码归一 | ✅ |
| **可评测 RAG** | 4 种切分策略可插拔 + 自建 80 条标注评估集，HitRate@K 可一键复现 | ✅ |
| **评测 harness** | Trace + 4 评测器 + G-Eval logprobs 加权 + Markdown 门禁报告 | ✅ |
| **网关治理链** | Filter Chain：认证 / Prompt 注入检测 / per-tenant 限流，fail-fast | ✅ |
| **后端底座** | 并发池 + 两级配额、多级缓存 + singleflight 防击穿、Trace 按月分表、评测幂等消费 + DLQ | 📋 |

> ✅ = 已实现且有测试 / 🚧 = 组件就位，运行时串联推进中 / 📋 = 设计完成待实现

## 快速开始

核心模块已实现（config 热更新 / mcp 适配 / rag 4 切分 / eval 评测器 / gateway 治理链），`go test ./...` 全绿。多 Agent 分层协作的运行时串联按 Roadmap 推进。

```bash
git clone https://github.com/XiaoLeng-master/agentloom.git
cd agentloom
make verify      # fmt + vet + 全量测试（含 race），三秒内全绿
make bench       # 跑切分策略 HitRate@K 对比表（实际可复现数字见 docs/03-benchmark.md）
make run         # 启动 HTTP 入口 agentd，监听 :8080
curl localhost:8080/health   # {"status":"ok","agents":["kb-agent"]}
```

> `make run` 起的 `agentd` 是最小 HTTP 表面，配置热更新 Manager 从启动即生效；Agent ReAct 循环、MCP 工具、评测闭环按模块逐步接进来。

环境：
- Go 1.22+
- （可选）Ollama + qwen2.5 / nomic-embed-text：把 `HashEmbedder` 换成真实 embedder，跑生产级 benchmark 数字；当前零依赖词法 embedder 即可复现方法论。
- Ollama + qwen2.5（Judge）+ nomic-embed-text（embedding）
- etcd / Redis / Milvus / Kafka（docker-compose 一键起）

## 仓库结构

```
agentloom/
├── README.md
├── docs/
│   ├── architecture.svg         # 架构图（源文件）
│   ├── 01-architecture.md        # 四面分层 + 数据流 + 闭环详解
│   ├── 02-design-decisions.md   # 6 个设计取舍（为什么这么做）
│   ├── 03-benchmark.md          # 切分策略 × Top-K 的 HitRate 表 + 复现命令
│   └── roadmap.md               # 10 天排期
├── cmd/                          # HTTP 入口（WIP）
├── internal/
│   ├── core/                     # ReAct、factory、registry
│   ├── config/                   # 热更新、引用计数安全替换
│   ├── mcp/                      # stdio/http transport、tool adapter
│   ├── rag/                      # splitter(4 策略)、embedding、store、retriever
│   ├── eval/                     # trace、evaluator、geval、report
│   ├── gateway/                  # filter chain（认证/注入检测/限流）
│   └── infra/                    # 并发池、缓存、mq、分表辅助
├── datasets/rag_eval_80.json     # 自建 80 条标注评估集（公开来源）
├── scripts/                      # bench.sh / eval.sh
├── examples/                     # 2 个可跑示例
└── Makefile
```

## 设计取舍（为什么这么做）

详见 [`docs/02-design-decisions.md`](docs/02-design-decisions.md)。核心 6 条：

1. **为什么热更新用 Watch + 引用计数**——新实例预热、旧实例 drain 后下线，做到零中断；纯 Watch 不够，会打断在跑请求。
2. **为什么评测规则优先、LLM-as-Judge 兜底**——规则可复现可解释，Judge 只在规则无因时启用，避免评测器本身飘。
3. **为什么按能力拆池而非按 Agent 拆**——真正高并发的不是"思考"，是"调工具、写回系统"。把稀缺 LLM 推理与大量 I/O 分离，降成本与尾延迟。
4. **为什么 Trace 走 Kafka 异步**——Trace 高频写入不能阻塞会话主链路；Kafka 还能回放做回归。
5. **为什么每次优化带"预测"**——预测的匹配/证伪比分数涨跌多一比特信息，能归因到"你对系统的理解"而非运气。
6. **为什么自建 80 条公开评估集**——私有数据不能开源，公开数据集可复现可审计，clone 下来就能跑。

## Roadmap

见 [`docs/roadmap.md`](docs/roadmap.md)。10 天排期，每天 2-3 小时，从骨架到可演示。

## License

[MIT](LICENSE)
