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
| **多 Agent 分层协作** | Planner / Executor / Verifier 角色池与消息契约；子任务当前串行执行 | ✅ |
| **Harness 自进化闭环** | 可手动触发同语料检索复测、确定性质量代理、引用失败统计、预测检验、门禁、本地配置生效与实验历史读回；真实 LLM Judge、Trace 驱动批评测及自动回滚仍未完成 | 🚧 |
| **预测式归因** | 每次优化提交声明预期收益，评测验证预测成立与否，逐次变更可归因 | 📋 |
| **配置热更新** | 进程内引用计数替换；etcd Source 尚未实现 | ✅ |
| **MCP 双传输适配** | stdio / HTTP JSON-RPC；请求取消、响应关联校验与有限 JSON Schema 参数校验；写工具默认不重试 | 🚧 |
| **可评测 RAG** | 4 种切分策略可插拔 + 自建 98 条标注评估集，HitRate@K 可一键复现 | ✅ |
| **评测 harness** | Trace + 规则代理评测器 + 词法测试 Prober + Markdown 报告；真实 Judge 尚未接入 | 🚧 |
| **网关治理链** | Filter Chain：本地演示 token、关键词注入信号、单机租户限流 | 🚧 |
| **插件化运行时** | IoC 生命周期：planner/memory/tool/evaluator 可热插拔，mount/reload/dispose 不重置 agent 状态 | ✅ |
| **后端底座** | 可选 Redis 持久化演示 Trace；内存队列、按月分组。Embed 与补全缓存默认 L1 LRU，设置 `AGENTLOOM_REDIS_ADDR` 后才用 Redis 作 L2，见 `docs/11-multilevel-cache.md`。Kafka、etcd、Milvus 与 DLQ 尚未接入 | 🚧 |

> ✅ = 已实现且有测试 / 🚧 = 组件就位，运行时串联推进中 / 📋 = 设计完成待实现

## 快速开始

核心模块已实现（config 热更新 / mcp 适配 / rag 4 切分 / eval 评测器 / gateway 治理链 / runtime 插件生命周期 / orchestrator 多 Agent 池 / infra 削峰分表），`go test ./...` 全绿。自进化闭环的端到端串联按 Roadmap 推进。

```bash
git clone https://github.com/XiaoLeng-master/agentloom.git
cd agentloom
make verify      # 格式整理 + vet + 全量 race 测试
make bench       # 输出当前提交的切分策略 HitRate@K 对比表
make cachebench  # 同一批请求在接入缓存前后的 embed / LLM 调用次数
make run         # 启动 HTTP 入口 agentd，监听 :8080
go run ./cmd/evolve # 离线运行 fixed→recursive 检索复测，输出逐条结果和配置版本
curl localhost:8080/health   # {"status":"ok","agents":["kb-agent"]}
```

`make run` 后打开 `http://localhost:8080/`，可在本地工作台使用 Chat、Trace、Benchmark 三个页面。Benchmark 首次通过只读 `GET /v1/bench` 展示基线，点击复测才调用会更新本地配置的 `POST /v1/evolve`。前端由同一个 Go 服务提供；在 `web/` 执行 `npm ci && npm run build` 可重新构建嵌入的静态资源（需 Node.js/npm）。仓库已包含构建产物，普通运行无需安装前端依赖。浏览器不接触 `DEEPSEEK_API_KEY`。

`cmd/evolve` 会在相同数据集和语料上计算两次检索命中率，并输出数据哈希、逐条命中、失败来源归因、预测区间、实际变化和本地配置版本。服务运行时可用本地演示 token 调用 `POST /v1/evolve`；每次实验写入本地 `.agentloom/evolutions/`，通过 `GET /v1/evolutions` 或 `GET /v1/evolutions/{id}` 读回。门禁通过后，后续 `/v1/chat` 请求的 Trace 会显示新的配置版本和切分策略。该接口是手动触发的检索实验，当前没有请求 Trace 驱动的定时评测；答案质量、安全性与自动回滚也尚未完成。数字随被索引的仓库文件变化，请以当次运行输出为准。

本地端到端演示（确定性演示模型、内存文档检索；不是在线 LLM 或生产知识库）：

```bash
make run
curl -H 'Authorization: Bearer local-demo' -H 'Content-Type: application/json' \
  -d '{"message":"planner executor"}' localhost:8080/v1/chat
# 从返回的 trace_id 查询执行契约：
curl -H 'Authorization: Bearer local-demo' localhost:8080/v1/traces/<trace_id>
```

演示链路经过本地网关、Planner、Executor 的 RAG Tool 和 Verifier。服务从公开的 README 和架构文档建立检索索引；Trace 的 `retrievals` 记录实际返回给 Executor 的片段和来源。初始配置为 fixed 切分（片段可能残缺），调用 `POST /v1/evolve` 通过检索与确定性质量代理门禁后切到 recursive；重启服务会重置为 fixed，但实验历史仍保留。默认使用确定性演示模型；设置本地环境变量 `DEEPSEEK_API_KEY` 后，Executor 使用 DeepSeek 官方 Chat Completions API 的 `deepseek-flash`（可用 `DEEPSEEK_MODEL` 覆盖），关闭 thinking 并限制 1200 tokens；演示 Planner 保持单任务规则，Verifier 使用确定性非空输出规则（并不验证答案事实正确性）。Key 仅由 Go 服务读取，请勿写入仓库或浏览器。DeepSeek 是生成模型，检索仍使用 HashEmbedder。Trace 在响应前写入进程内存；如需跨进程保留，可执行 `docker compose up -d redis`，再设置 `AGENTLOOM_REDIS_ADDR=127.0.0.1:16379` 启动服务。后台队列异步写 Redis，请求不等待持久化；队列满或写入失败可能丢失跨进程记录，关停时会排空已接收事件。Redis 启动检查失败会报错。当前仍未接入真实 LLM Judge、Trace 驱动定时批评测与自动回滚；Kafka、etcd、Milvus 仍未落地。

环境：
- Go 1.22+
- 运行已构建工作台仅需 Go 1.22+；修改前端需 Node.js/npm。真实模型可选接入，外部基础设施仍在路线图中。

## 仓库结构

```
agentloom/
├── README.md
├── docs/
│   ├── architecture.svg         # 架构图（源文件）
│   ├── 01-architecture.md        # 四面分层 + 数据流 + 闭环详解
│   ├── 02-design-decisions.md   # 6 个设计取舍（为什么这么做）
│   ├── 03-benchmark.md          # 指标口径与复现命令
│   ├── 04-dataset-construction.md # 98 条评估集怎么标的、怎么扩展
│   ├── 05-evaluation-method.md  # 4 评测器算法 + 门禁规则 + 怎么跑
│   ├── 06-demo-script.md        # 可复现演示脚本
│   └── roadmap.md               # 10 天排期
├── cmd/                          # HTTP 入口（WIP）
├── internal/
│   ├── core/                     # ReAct、factory、registry
│   ├── config/                   # 热更新、引用计数安全替换
│   ├── mcp/                      # stdio/http transport、tool adapter
│   ├── rag/                      # splitter(4 策略)、embedding、store、retriever
│   ├── eval/                     # trace、evaluator、geval、report
│   ├── gateway/                  # filter chain（认证/注入检测/限流）
│   ├── runtime/plugin/           # IoC 插件生命周期（mount/reload/dispose）
│   ├── orchestrator/             # 多 Agent 池（planner/executor/verifier + 消息契约）
│   └── infra/                    # 削峰队列、按月分表、prefix/embed 缓存
├── datasets/rag_eval_98.json     # 自建 98 条标注评估集（公开来源）
├── scripts/                      # bench.sh 与数据集构造脚本
├── examples/                     # 预留示例目录
└── Makefile
```

## 设计取舍（为什么这么做）

详见 [`docs/02-design-decisions.md`](docs/02-design-decisions.md)。核心 6 条：

1. **为什么热更新用 Watch + 引用计数**——新实例预热、旧实例 drain 后下线，做到零中断；纯 Watch 不够，会打断在跑请求。
2. **为什么评测规则优先、LLM-as-Judge 兜底**——规则可复现可解释，Judge 只在规则无因时启用，避免评测器本身飘。
3. **为什么按能力拆池而非按 Agent 拆**——真正高并发的不是"思考"，是"调工具、写回系统"。把稀缺 LLM 推理与大量 I/O 分离，降成本与尾延迟。
4. **为什么 Trace 走 Kafka 异步**——Trace 高频写入不能阻塞会话主链路；Kafka 还能回放做回归。
5. **为什么每次优化带"预测"**——预测的匹配/证伪比分数涨跌多一比特信息，能归因到"你对系统的理解"而非运气。
6. **为什么自建 98 条公开评估集**——私有数据不能开源，公开数据集可复现可审计，clone 下来就能跑。

## Roadmap

见 [`docs/roadmap.md`](docs/roadmap.md)。10 天排期，每天 2-3 小时，从骨架到可演示。

## License

[MIT](LICENSE)
