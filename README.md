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
| **Harness 自进化闭环** | `make loop` 与 `POST /v1/evolve` 在固定 98 条上做离线复测：模拟调用记录归因到检索/Prompt/工具层，结构化任务生成候选，再用来源命中、词法代理、引用失败和归因失败数做门禁。不是 Chat 运行时 Trace，不是生成答案质量，也不是 LLM Judge。门禁通过后进入进程内灰度：按租户哈希分流，观测窗口回落自动回滚，稳定则从 10% 放到 50% 再放到 100%。`make canary` 用命令给定的窗口复现这两条路径，不是 98 条公开集的在线测量。etcd 只把外部配置送进这套进程内灰度，不单独计算 98 条指标 | 🚧 |
| **预测式归因** | 每个优化任务在复测前用该层失败占比乘固定系数声明区间，复测后记录预测是否成立。预测未命中会留下，但不能单独决定晋升 | 🚧 |
| **配置热更新** | 进程内引用计数替换。设置 `AGENTLOOM_ETCD_ENDPOINTS` 时由 etcd Watch 触发 Reload/Drain；灰度比例、放量和一键回滚复用同一套方法。未设置端点时使用内存配置 | ✅ |
| **MCP 双传输适配** | stdio / HTTP JSON-RPC；请求取消、响应关联校验与有限 JSON Schema 参数校验；写工具默认不重试 | 🚧 |
| **可评测 RAG** | 4 种切分策略可插拔 + 自建 98 条标注评估集，HitRate@K 可一键复现 | ✅ |
| **评测 harness** | Trace + 规则代理评测器 + 词法测试 Prober + Markdown 报告；真实 Judge 尚未接入 | 🚧 |
| **网关治理链** | Filter Chain：本地演示 token、关键词注入信号、单机租户限流 | 🚧 |
| **插件化运行时** | IoC 生命周期：planner/memory/tool/evaluator 可热插拔，mount/reload/dispose 不重置 agent 状态 | ✅ |
| **后端底座** | 可选 Redis 持久化演示 Trace；可选 etcd 配置热更新；内存队列、按月分组及内存缓存；Kafka、Milvus 与 DLQ 尚未接入 | 🚧 |

> ✅ = 已实现且有测试 / 🚧 = 组件就位，运行时串联推进中 / 📋 = 设计完成待实现

## 快速开始

核心模块已实现（config 热更新 / mcp 适配 / rag 4 切分 / eval 评测器 / gateway 治理链 / runtime 插件生命周期 / orchestrator 多 Agent 池 / infra 削峰分表），`go test ./...` 全绿。自进化闭环的端到端串联按 Roadmap 推进。

```bash
git clone https://github.com/XiaoLeng-master/agentloom.git
cd agentloom
make verify      # 格式整理 + vet + 全量 race 测试
make bench       # 输出当前提交的切分策略 HitRate@K 对比表
make loop        # 98 条评测→归因聚类→多候选→自动复测门禁（确定性替身，不调用外部模型）
make canary      # 进程内灰度：给定窗口复现放量和回滚（不是公开集实测，也不是在线流量）
make etcd-demo   # 本机 Docker 起单节点 etcd 并前台启动 agentd；不设端点时 make run 仍用内存配置
make run         # 启动 HTTP 入口 agentd，监听 :8080
go run ./cmd/evolve # 与 make loop 相同的任务驱动闭环，输出归因、候选和门禁报告
curl localhost:8080/health   # {"status":"ok","agents":["kb-agent"]}
```

`make run` 后打开 `http://localhost:8080/`，可在本地工作台使用 Chat、Trace、Benchmark 三个页面。Benchmark 首次通过只读 `GET /v1/bench` 展示基线，点击复测才调用会更新本地配置的 `POST /v1/evolve`。前端由同一个 Go 服务提供；在 `web/` 执行 `npm ci && npm run build` 可重新构建嵌入的静态资源（需 Node.js/npm）。仓库已包含构建产物，普通运行无需安装前端依赖。浏览器不接触 `DEEPSEEK_API_KEY`。

`make loop` 和 `go run ./cmd/evolve` 跑同一条离线闭环：固定 98 条、按原因聚类，以及每个候选的层级、原因、配置和预测区间。评测器自己检索并渲染回答，不经过 Planner/Executor/Verifier。来源命中、词法代理（`deterministic_proxy`：groundTruth 与首个检索片段的词法重叠）、渲染回答里的事实覆盖是三项不同指标。门禁比较来源命中不降、词法代理不降（1e-6）、不新增引用失败、归因失败数不增加，并且至少一项硬指标变好；只把回答变短不能晋升。预测未命中会写入结果，但不单独决定晋升。工具层失败在公开基线的 search 策略下不会出现，只在注入的工具模式测试里出现。服务运行时，本地管理员令牌 `local-demo` 可以调用 `POST /v1/evolve`；公开只读访问不需要这个令牌。响应里的 `tasks` 是结构化任务。实验写入本地 `.agentloom/evolutions/`。语料包含 README 和部分源文件，改这些文件会改变哈希和指标，数字以该提交的 `make loop` 输出为准。在这句话写入时的测量里，基线 98 条全部被归因为失败：检索层包含来源未命中和“来源命中但上下文没有 ground truth”，Prompt 层与工具层为 0。唯一晋升的是提高 TopK 的检索任务，来源命中上升且该任务的预测命中，归因失败数从 98 降到 97。semantic 与 parent-child 的预测未命中，未命中不会被当成通过。recursive 的预测命中，但词法代理下降，门禁拒绝。没有定时评测。门禁通过后候选进入进程内灰度，不整进程替换稳定配置。`make canary` 用命令给定的观测窗口分别复现灰度到放量和灰度到回滚；这些窗口不是 98 条公开集的测量，也不是在线流量。设置 `AGENTLOOM_ETCD_ENDPOINTS` 后，etcd 中的稳定配置走 Reload/Drain，灰度文档走同一套比例分流和回滚；未设置时仍是内存配置。本地步骤见 [`docs/10-etcd-hot-reload.md`](docs/10-etcd-hot-reload.md)。

本地端到端演示（确定性演示模型、内存文档检索；不是在线 LLM 或生产知识库）：

```bash
make run
curl -H 'Authorization: Bearer local-demo' -H 'Content-Type: application/json' \
  -d '{"message":"planner executor"}' localhost:8080/v1/chat
# 从返回的 trace_id 查询执行契约：
curl -H 'Authorization: Bearer local-demo' localhost:8080/v1/traces/<trace_id>
```

演示链路经过本地网关、Planner、Executor 的 RAG Tool 和 Verifier。服务从公开的 README 和架构文档建立检索索引；Trace 的 `retrievals` 记录实际返回给 Executor 的片段和来源。初始配置为 fixed 切分。`POST /v1/evolve` 按当次归因生成候选，只有硬指标门禁通过才把候选放进灰度，不整进程替换稳定配置，也不会固定改成 recursive。已有灰度时再次调用按稳定配置复测，不重复启动同一候选。连续调用时，基线是当前稳定配置的切分、Prompt 渲染模式和 TopK，而不是重新从 echo/top-3 起算。重启服务会重置配置，实验历史仍保留。本地模式的管理员令牌是 `local-demo`；公开模式才读取 `AGENTLOOM_ADMIN_TOKEN` 和可选的 `AGENTLOOM_PUBLIC_TOKEN`。默认使用确定性演示模型；设置本地环境变量 `DEEPSEEK_API_KEY` 后，Executor 使用 DeepSeek 官方 Chat Completions API 的 `deepseek-flash`（可用 `DEEPSEEK_MODEL` 覆盖），关闭 thinking 并限制 1200 tokens；演示 Planner 保持单任务规则，Verifier 使用确定性非空输出规则（并不验证答案事实正确性）。Key 仅由 Go 服务读取，请勿写入仓库或浏览器。DeepSeek 是生成模型，检索仍使用 HashEmbedder。这一默认模型配置只验证了请求构造，没有在本仓库里用真实 Key 做端到端验收。Trace 在响应前写入进程内存；如需跨进程保留，可执行 `docker compose up -d redis`，再设置 `AGENTLOOM_REDIS_ADDR=127.0.0.1:16379` 启动服务。后台队列异步写 Redis，请求不等待持久化；队列满或写入失败可能丢失跨进程记录，关停时会排空已接收事件。Redis 启动检查失败会报错。当前仍未接入真实 LLM Judge 和 Trace 驱动的定时评测。进程内灰度与自动回滚由 `make canary` 用给定窗口复现。etcd 热更新是可选本地组件，见 [`docs/10-etcd-hot-reload.md`](docs/10-etcd-hot-reload.md)；Kafka、Milvus 仍未落地。

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
│   ├── 07-public-demo.md        # 公开演示的访问边界
│   ├── 10-etcd-hot-reload.md    # 可选 etcd 热更新、灰度与回滚
│   └── roadmap.md               # 10 天排期
├── cmd/                          # HTTP 入口（WIP）
├── internal/
│   ├── core/                     # ReAct、factory、registry
│   ├── config/                   # 热更新、引用计数、可选 etcd Source
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
