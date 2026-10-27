# 06 · 演示脚本

> 录一个 3-5 分钟的 demo 视频，让开发者 clone 后能照着复现。
> 每个分镜给出命令 + 旁白要点。建议用 asciinema 录终端，配字幕。

## 录制前准备

```bash
git clone https://github.com/XiaoLeng-master/agentloom.git
cd agentloom
# 可选：go 版本 >= 1.22，无需任何外部依赖
```

## 分镜

### 镜 1 · 全绿测试（20s）

**命令**：
```bash
make verify
```

**旁白要点**：
- 9 个包全绿，含 race 检测
- plugin 模块的 `TestAgentStateSurvivesPluginReload` 是核心——证明插件热更新不动 agent 会话状态
- orchestrator 的 `TestConcurrentRuns` 验证多 Agent 池并发安全

### 镜 2 · RAG benchmark（30s）

**命令**：
```bash
make bench
```

**旁白要点**：
- 98 条公开评估集，HitRate@K 一键复现
- 读取本次命令实际输出，不预设某个策略胜出
- 数字来自零依赖词法 embedder；真实模型效果需要单独验证

### 镜 3 · HTTP 入口（15s）

**命令**：
```bash
make run &
sleep 1
curl localhost:8080/health
# {"status":"ok","agents":["kb-agent"]}
```

**旁白要点**：
- agentd 是最小 HTTP 表面，配置热更新从启动即生效
- /health 返回已注册 agent，证明 Factory/Registry 工作

### 镜 4 · 插件生命周期（40s）

**命令**（可写个 examples/plugin_lifecycle.go，或直接展示测试）：
```bash
go test -run TestAgentStateSurvivesPluginReload -v ./internal/runtime/plugin/
```

**旁白要点**：
- Mount 一个 tool 插件 → Acquire 拿引用 → Reload 换新版本 → 旧 handle 仍服务旧插件（零中断）→ Release 后 Dispose 旧版
- **关键**：agent 的 conversation 数组在插件 reload 前后不变，验证状态与插件资源隔离
- 引用计数保证在用插件不被拆

### 镜 5 · 多 Agent 池协作（40s）

**命令**：
```bash
go test -run TestHappyPath -v ./internal/orchestrator/
```

**旁白要点**：
- Planner 池规划子任务 → Executor 池 round-robin 执行 → Verifier 池校验
- Agent 间只通过 Contract 消息契约交接，不共享内部状态
- Verifier 判 fail 会回流让 Planner 重规划（`TestReplanOnVerifyFail`）

### 镜 6 · Trace 削峰 + 按月分表（30s）

**命令**：
```bash
go test -run TestQueueBurstThenDrain -v ./internal/infra/
```

**旁白要点**：
- ToyQueue 模拟 Kafka 削峰：session 主链路 Push 不阻塞，满则丢弃+计数
- MonthlySharder 按时间戳 YYYY-MM 路由，eval 报告能查"某月的 trace"
- 配置 `AGENTLOOM_KAFKA_BROKERS` 后走同一路由键，见 `docs/09-kafka-trace.md`

### 镜 7 · 收尾 + 仓库导览（15s）

**命令**：
```bash
tree -L 2 internal/  # 展示模块结构
```

**旁白要点**：
- 7 个 internal 模块 + 2 个 cmd 入口
- README 状态表标了每个特性的实现度（✅/🚧/📋），不藏
- Roadmap 在 docs/roadmap.md，自进化闭环的端到端串联是下一步

## 录制建议

- 用 [asciinema](https://asciinema.org) 录终端，导出 GIF 或嵌入 README
- 每镜命令先空跑确认输出干净再正式录
- 总长 3-5 分钟，超过 5 分钟没人看完
- 字幕用简洁陈述句，不要念代码

## 可选：自动录

写个 `scripts/demo.sh` 把上面 7 镜命令串起来，asciinema rec 一遍过：

```bash
bash scripts/demo.sh | asciinema rec demo.cast
# 转 GIF: agg demo.cast demo.gif
```
