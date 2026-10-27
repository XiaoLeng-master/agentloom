# 公开演示运行说明

AgentLoom 的公开演示边界是“可访问、可审计、可停止”的作品集环境，不等同于生产多租户服务。

## 本地模式

不设置 `AGENTLOOM_PUBLIC_MODE` 时，Chat、Trace 和只读 Benchmark 可直接访问。`POST /v1/evolve` 的管理员令牌由进程内固定为 `local-demo`。`cmd/agentd` 只在公开模式读取 `AGENTLOOM_ADMIN_TOKEN`；在本地模式 export 它不会改变令牌。

```bash
make run
curl -H 'Authorization: Bearer local-demo' -X POST localhost:8080/v1/evolve
```

## 公开模式

公开部署至少设置管理员令牌；如果希望访客也必须登录，再设置公开令牌：

```bash
export AGENTLOOM_PUBLIC_MODE=1
export AGENTLOOM_ADMIN_TOKEN='替换为随机长令牌'
export AGENTLOOM_PUBLIC_TOKEN='可选的访客令牌'
export DEEPSEEK_API_KEY='只放在服务端环境变量'
export DEEPSEEK_MODEL=deepseek-flash
make run
```

公开模式下 Chat 的并发槽位为 4，网关按来源地址限制为每分钟 60 次；`POST /v1/evolve` 只接受管理员令牌。浏览器只发送用户输入的访问令牌，不接触 DeepSeek API Key。反向代理部署时应正确传递真实来源地址，且仍需在代理层补充 TLS、请求体大小和更严格的速率限制。

## 仍未声称完成的能力

- 当前分开计三项：来源命中、确定性词法代理已评（检索片段与标注的词法重合、引用失败和通过率）、真实回答未评。尚未接入真实 LLM Judge。四种切分的实测表见 README 与 `docs/05-evaluation-method.md`。
- 没有用真实 DeepSeek 跑过 Chat 或 evolve 的端到端请求。是否补跑由作者决定，调用官方 API 会产生费用。测试和 `go run ./cmd/bench` 不读取 `DEEPSEEK_API_KEY`。
- 可运行的 `agentd` 是单进程。热更新、限流和内存队列都在这个进程里，没有改成多实例或分布式。
- Trace 已可查询并异步写入内存或 Redis，但尚未实现 Trace 触发的自动批评测、灰度发布和自动回滚。
- Kafka、etcd、Milvus、用户系统、多租户和生产级密钥管理不属于当前公开演示的已交付范围。
