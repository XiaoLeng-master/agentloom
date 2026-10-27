# 公开演示运行说明

AgentLoom 的公开演示边界是“可访问、可审计、可停止”的作品集环境，不等同于生产多租户服务。

## 本地模式

不设置 `AGENTLOOM_PUBLIC_MODE` 时，Chat、Trace 和只读 Benchmark 可直接访问。`POST /v1/evolve` 的管理员令牌由进程内固定为 `local-demo`。`cmd/agentd` 只在公开模式读取 `AGENTLOOM_ADMIN_TOKEN` 和 `AGENTLOOM_PUBLIC_TOKEN`；在本地模式 export 它们不会改变令牌。

```bash
make run
curl -H 'Authorization: Bearer local-demo' -X POST localhost:8080/v1/evolve
```

本地和公开模式共用一道 Chat 在途准入：全局槽位默认 4，同一来源默认也是 4。超过后请求排队，直到有空位或客户端取消/超时，超时返回 429。按来源的滑动窗口仍是每分钟 60 次，超额立即拒绝，不排队。`AGENTLOOM_CHAT_GLOBAL` 与 `AGENTLOOM_CHAT_PER_SOURCE` 可覆盖槽位。这道门和 Executor 回合内的全局/按 Agent 信号量不是同一个计数器，见 [`docs/08-concurrency-quota.md`](08-concurrency-quota.md)。

## 公开模式

公开部署至少设置管理员令牌；如果希望访客也必须登录，再设置公开令牌。只有 `AGENTLOOM_PUBLIC_MODE=1` 时入口才会读取并应用这两个变量：

```bash
export AGENTLOOM_PUBLIC_MODE=1
export AGENTLOOM_ADMIN_TOKEN='替换为随机长令牌'
export AGENTLOOM_PUBLIC_TOKEN='可选的访客令牌'
export DEEPSEEK_API_KEY='只放在服务端环境变量'
export DEEPSEEK_MODEL=deepseek-flash
make run
```

公开模式不再另外做一次容量为 4 的立刻拒绝。`POST /v1/evolve` 只接受管理员令牌。浏览器只发送用户输入的访问令牌，不接触 DeepSeek API Key。反向代理部署时应正确传递真实来源地址，且仍需在代理层补充 TLS、请求体大小和更严格的速率限制。

## 仍未声称完成的能力

- 当前质量评测是 `deterministic_proxy`，用于复现检索上下文、引用和词法重合；尚未接入真实 LLM Judge。
- Trace 已可查询并异步写入内存或 Redis，但尚未实现 Trace 触发的自动批评测、灰度发布和自动回滚。
- Kafka、etcd、Milvus、用户系统、多租户和生产级密钥管理不属于当前公开演示的已交付范围。
