#!/usr/bin/env python3
"""Generate datasets/rag_eval_80.json — a self-built, public-source RAG
evaluation set for agentloom.

The 80 questions are derived from agentloom's own public docs (README,
architecture, design decisions) and general Agent/Go engineering concepts.
No proprietary data. Each entry has:
  id         — eval_001..eval_080
  type       — retrieval | qa | synthesis | security
  question   — the user query
  groundTruth — the expected answer (short, factual)
  sourceDoc  — which public doc the answer comes from
Run: python3 scripts/gen_dataset.py   (writes datasets/rag_eval_80.json)
"""
import json, os, pathlib

SEEDS = [
    # (type, question, groundTruth, sourceDoc)
    ("retrieval", "agentloom 的四层架构是哪四层？", "控制面、执行面、记忆面、观测面", "docs/01-architecture.md"),
    ("retrieval", "ReAct 循环的四个阶段是什么？", "Think、Act、Observe、下一轮 Think", "internal/core/agent.go"),
    ("retrieval", "DefaultMaxIter 的默认值是多少？", "10", "internal/core/agent.go"),
    ("retrieval", "Agent 接口暴露哪三个方法？", "Name、Schema、Call", "internal/core/tool.go"),
    ("retrieval", "LLMClient 接口的方法名是什么？", "Complete", "internal/core/llm.go"),
    ("retrieval", "Message 的四种 Role 是什么？", "system、user、assistant、tool", "internal/core/message.go"),
    ("retrieval", "ToolCall 包含哪三个字段？", "ID、Name、Args", "internal/core/message.go"),
    ("retrieval", "Registry 的三个方法是什么？", "Register、Create、Names", "internal/core/factory.go"),
    ("retrieval", "配置热更新用哪个组件做发布通道？", "etcd（生产 Source）", "internal/config/source.go"),
    ("retrieval", "AgentHandle 的 IsStale 判断什么？", "当前配置是否已被新版本取代", "internal/config/config.go"),
    ("retrieval", "Manager.Drain 的作用是什么？", "等待被取代 entry 的引用计数归零", "internal/config/config.go"),
    ("retrieval", "Reload 返回值是什么？", "被取代版本的在途引用计数", "internal/config/config.go"),
    ("retrieval", "MCP 有哪两种传输？", "stdio 和 HTTP", "internal/mcp/transport.go"),
    ("retrieval", "StdioTransport 如何防半读？", "按行分隔 JSON 消息", "internal/mcp/stdio.go"),
    ("retrieval", "MCPTool 实现哪个接口？", "core.Tool", "internal/mcp/adapter.go"),
    ("retrieval", "AdvertiseAll 的作用？", "把整个 MCP server 的工具转成 core.Tool 集", "internal/mcp/adapter.go"),
    ("retrieval", "RAG 有哪四种切分策略？", "Fixed、Recursive、Semantic、ParentChild", "internal/rag/splitter.go"),
    ("retrieval", "ParentChild 切分为什么更优？", "小块精准检索、父块注入完整上下文", "internal/rag/splitter.go"),
    ("retrieval", "HashEmbedder 的作用？", "无依赖确定性 embedder，让 HitRate 差异归因到切分", "internal/rag/embedding.go"),
    ("retrieval", "MemoryStore 用什么相似度？", "cosine similarity", "internal/rag/store.go"),
    ("retrieval", "Retriever.Retrieve 返回什么？", "Top-K 上下文（Context 列表）", "internal/rag/retriever.go"),
    ("retrieval", "自进化闭环有几环？", "7 环：Trace 采集→Kafka→评测→归因→优化→复测→热更新", "docs/01-architecture.md"),
    ("retrieval", "预测式归因比分数涨跌多什么信息？", "多一比特归因信息（预测匹配/证伪）", "docs/02-design-decisions.md"),
    ("retrieval", "评测器为什么规则优先、Judge 兜底？", "规则可复现，避免评测器本身飘带偏闭环", "docs/02-design-decisions.md"),
    ("retrieval", "后端底座横切哪几项？", "并发池、MQ削峰、分表、幂等、缓存一致性", "docs/01-architecture.md"),
    ("retrieval", "Agent 请求放大效应是什么？", "1 请求放大为多次 LLM/工具/检索下游调用", "docs/02-design-decisions.md"),
    ("retrieval", "singleflight 防什么？", "缓存击穿（stampede）", "docs/02-design-decisions.md"),
    ("retrieval", "多 Agent 按什么拆池？", "按能力（Planner/Executor/Verifier）而非按 Agent", "docs/01-architecture.md"),
    ("retrieval", "Verifier 池用什么模型？", "规则 + 小模型", "docs/01-architecture.md"),
    ("retrieval", "Planner 池的特点？", "贵模型、少而精、负责拆任务", "docs/01-architecture.md"),
    ("retrieval", "Executor 池的特点？", "平价模型、横向扩、调工具写回", "docs/01-architecture.md"),
    ("retrieval", "G-Eval 用什么加权打分？", "候选评分 Token 的概率分布归一化加权", "docs/02-design-decisions.md"),
    ("retrieval", "Trace 为什么走 Kafka？", "高频写不阻塞主链路 + 可回放做回归", "docs/02-design-decisions.md"),
    ("retrieval", "项目用什么语言实现？", "Go", "README.md"),
    ("retrieval", "项目 License 是什么？", "MIT", "LICENSE"),
    ("retrieval", "agentloom 的 GitHub 地址？", "github.com/XiaoLeng-master/agentloom", "README.md"),
    ("retrieval", "10 天 Roadmap 的 D1 产出什么？", "core：ReAct + factory/registry + 单测", "docs/roadmap.md"),
    ("retrieval", "D2 的产出？", "config 热更新 + 引用计数 + 并发单测", "docs/roadmap.md"),
    ("retrieval", "D3 的产出？", "mcp stdio/http 适配 + 统一 Tool", "docs/roadmap.md"),
    ("retrieval", "D5 的产出？", "自建 80 条评估集 + bench 脚本", "docs/roadmap.md"),
    ("retrieval", "D7 的产出？", "eval：Trace + 4 评测器 + G-Eval", "docs/roadmap.md"),
    ("qa", "ReAct 循环为什么设迭代上限？", "防止模型反复调用工具导致死循环", "internal/core/agent.go"),
    ("qa", "context 为什么贯穿工具调用？", "HTTP 取消能逐层 unwind 到每个工具", "internal/core/agent.go"),
    ("qa", "未知工具为什么不 panic？", "作为 Observation 回喂模型自我纠正", "internal/core/agent.go"),
    ("qa", "配置热更新为什么不直接重启？", "重启会打断在跑的 ReAct 循环", "internal/config/config.go"),
    ("qa", "引用计数怎么保证不丢请求？", "旧实例在途请求归零后才下线", "internal/config/config.go"),
    ("qa", "etcd Watch 单独够不够？", "不够，会打断在跑请求，需配合引用计数", "internal/config/config.go"),
    ("qa", "stdio 和 HTTP 传输什么场景用？", "stdio 本地工具，HTTP 远程 MCP server", "internal/mcp/stdio.go"),
    ("qa", "MCP 适配层最难的是什么？", "工具 schema 不一致、超时与错误码归一", "internal/mcp/adapter.go"),
    ("qa", "Fixed 切分的问题是什么？", "可能切断语义，检索匹配不精准", "internal/rag/splitter.go"),
    ("qa", "Semantic 切分基于什么？", "句子边界（. ! ? 换行）", "internal/rag/splitter.go"),
    ("qa", "Recursive 的 overlap 作用？", "保留边界上下文，避免切断丢信息", "internal/rag/splitter.go"),
    ("qa", "为什么自建评估集而不是用公司数据？", "公司数据不能开源，公开集可复现可审计", "docs/02-design-decisions.md"),
    ("qa", "HitRate@K 的 K 取几？", "3", "docs/03-benchmark.md"),
    ("qa", "HitRate@K 怎么算命中？", "前 K 条结果包含与 groundTruth 同源文档", "docs/03-benchmark.md"),
    ("qa", "评测器规则优先的好处？", "可复现、可解释、零成本", "docs/02-design-decisions.md"),
    ("qa", "LLM-as-Judge 何时启用？", "规则无法归因时兜底", "docs/02-design-decisions.md"),
    ("qa", "为什么按能力拆池而非按 Agent？", "高并发的是 I/O 不是思考，分离降成本", "docs/01-architecture.md"),
    ("qa", "缓存一致性怎么保证？", "版本号 + 变更事件失效，宁可 miss 不可脏读", "docs/01-architecture.md"),
    ("qa", "Trace 按什么分表？", "按月分表 + 冷热分离", "docs/01-architecture.md"),
    ("qa", "评测任务为什么幂等？", "重试不重复副作用，避免污染数据", "docs/01-architecture.md"),
    ("qa", "Agent 健壮性的三点是什么？", "迭代上限、context 贯穿、失败回喂不崩", "internal/core/agent.go"),
    ("synthesis", "ReAct 循环 + 引用计数热更新如何配合？", "热更新保证 ReAct 在跑任务不被打断", "docs/01-architecture.md"),
    ("synthesis", "评测闭环 + 热更新如何咬合？", "评测通过的优化经热更新灰度生效", "docs/01-architecture.md"),
    ("synthesis", "多 Agent 池 + 网关链如何协作？", "Agent 间调用走同一网关治理链", "docs/01-architecture.md"),
    ("synthesis", "RAG 评测 + 自进化闭环的关系？", "RAG 是闭环的第一个优化对象", "docs/01-architecture.md"),
    ("synthesis", "请求放大效应 + 并发池的关系？", "并发池控制放大后的下游压力", "docs/02-design-decisions.md"),
    ("synthesis", "预测式归因 + 闭环的关系？", "预测验证驱动逐次优化归因", "docs/02-design-decisions.md"),
    ("synthesis", "parent-child + 缓存如何配合？", "检索小块缓存精准、注入父块完整", "internal/rag/splitter.go"),
    ("synthesis", "Trace + Kafka + 评测的关系？", "Trace 经 Kafka 异步落评测，不阻塞主链", "docs/01-architecture.md"),
    ("synthesis", "Factory/Registry + 热更新的关系？", "Registry 是热更新的承载替换点", "internal/config/config.go"),
    ("synthesis", "MCP + 统一 Tool 接口的关系？", "MCP 适配层把远程工具收敛为统一 Tool", "internal/mcp/adapter.go"),
    ("synthesis", "Executor 池 + 工具调用的关系？", "Executor 承载大量工具 I/O 横向扩", "docs/01-architecture.md"),
    ("synthesis", "Verifier + 敏感操作的关系？", "Verifier 拦截敏感操作不靠模型自觉", "docs/01-architecture.md"),
    ("synthesis", "Drain + Reload 的协作？", "Reload 换新、Drain 等旧归零", "internal/config/config.go"),
    ("synthesis", "singleflight + 多级缓存的关系？", "互斥重建防多 worker 同时算同 key", "docs/02-design-decisions.md"),
    ("synthesis", "评测门禁 + 灰度的关系？", "门禁通过才灰度上线，防回归", "docs/01-architecture.md"),
    ("synthesis", "bad case 池 + 优化任务的关系？", "bad case 归因产出优化 diff", "docs/01-architecture.md"),
    ("security", "Prompt 注入检测在哪个层？", "网关 Filter Chain", "docs/01-architecture.md"),
    ("security", "限流分哪两级？", "全局 + 租户", "docs/01-architecture.md"),
    ("security", "多租户怎么隔离？", "存储分片 + 网关配额 + 会话 ID 前缀路由", "docs/01-architecture.md"),
    ("security", "写工具调用如何授权？", "pending plan + 确认签名才执行", "internal/core/tool.go"),
    ("security", "敏感信息如何处理？", "Trace 报告脱敏，不存 API key", "docs/01-architecture.md"),
    ("security", "未知工具如何处置？", "不执行，错误回喂模型", "internal/core/agent.go"),
    ("security", "超时如何传播？", "context 从 HTTP 顶层贯穿到工具", "internal/core/agent.go"),
    ("security", "评测报告为何脱敏？", "避免原始模型响应或私有内容外泄", "docs/01-architecture.md"),
]


def main():
    out = []
    for i, (typ, q, gt, src) in enumerate(SEEDS, start=1):
        out.append({
            "id": f"eval_{i:03d}",
            "type": typ,
            "question": q,
            "groundTruth": gt,
            "sourceDoc": src,
        })
    if len(out) < 80:
        raise SystemExit(f"need >=80 seeds, got {len(out)}")
    out = out[:80]
    here = pathlib.Path(__file__).resolve().parent.parent
    dst = here / "datasets" / "rag_eval_80.json"
    dst.parent.mkdir(parents=True, exist_ok=True)
    dst.write_text(json.dumps(out, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"wrote {len(out)} cases to {dst}")


if __name__ == "__main__":
    main()
