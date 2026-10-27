# 11 · 多级缓存

Embed 与 LLM 补全都走可选的两级缓存。L1 是进程内 LRU，带 TTL。L2 是 Redis。没设置 `AGENTLOOM_REDIS_ADDR` 时只有 L1；设置之后，Trace 用的同一个 Redis 也作为 L2。`compose.yaml` 里已有的 `redis` 服务就够，没有另建 compose 文件。

`GET /v1/metrics` 和 `GET /v1/bench` 的 `cache` 字段按 L1、L2 分开给出 hits、misses、hit_rate。L2 未启用时 `enabled` 为 false。

## 为什么 LLM 用应用层 PrefixCache

`internal/infra.PrefixCache` 按整段消息列表的哈希保存上一次补全结果。命中时不再调用 `LLMClient.Complete`。

这和供应商的 prompt caching 不是一回事。供应商缓存（例如服务端保留前缀 KV）仍然会发出请求，只是可能少算前缀 token。当前 `DeepSeekClient` 没有发送这类参数，响应里也不读缓存 token。验收要的是调用次数下降，所以这里接的是现有的 PrefixCache，而不是改成依赖供应商缓存。

Embed 则把 `EmbedCache` 接进 `rag.Retriever`。相同文本命中后不再调用 `Embedder.Embed`。`Sync` / `Index` 用文档正文的 SHA-256 判断变化：只有哈希变了的文档会重新切分和 embed，旧 chunk 会从缓存里删掉，向量库里也只替换该来源。

## 同一批请求的调用次数

命令：

```bash
go run ./cmd/cachebench
```

环境：Go 1.22.2 linux/amd64，2026-10-27T09:09:56Z（UTC）。这组数字对应提交 `048acd751eb9c671077c9e6d8a9b08083f27d2e9`（父提交 `2291d87`）。没有设置 Redis，没有调用付费 API。Embedder 是 `HashEmbedder`（Dim 128）。LLM 是进程内计数器。批次是 2 篇文档、2 条查询、重复 2 遍，外加 4 次完全相同的消息列表。

| 项目 | 不接缓存 | 接上 L1 后 |
|---|---|---|
| Embedder.Embed 调用 | 12 | 6 |
| LLMClient.Complete 调用 | 4 | 1 |

接上 L1 之后，Embed 的 L1 是 6 次 miss、2 次 hit，hit_rate 0.25。第二次索引发现文档哈希没变，不再 embed；第二次查询命中，所以比 12 少了 6 次调用。Prefix 的 L1 是 1 次 miss、3 次 hit，hit_rate 0.75，因此 4 次相同补全只打到内层 1 次。

这些数字只描述这次命令。改了 `MeasureCacheSavings` 的文档或查询后要重跑。

## 增量重建

`go test -count=1 -run TestSyncReembedsOnlyChangedDocument ./internal/rag/` 构造 alpha、beta 两篇文档，再只改 alpha 的正文。断言里：新产生的 embed 文本不包含 beta 的旧 chunk，事件是 alpha=`updated`、beta=`unchanged`，alpha 的旧 chunk 已从缓存删除，beta 的 chunk 仍在。

## Redis

```bash
docker compose up -d redis
export AGENTLOOM_REDIS_ADDR=127.0.0.1:16379
export AGENTLOOM_CACHE_TTL=30m
make run
```

`AGENTLOOM_CACHE_TTL` 默认 `30m`，设为 `0s` 表示条目不过期。

集成测试带 `//go:build redis`。普通 `go test ./...` 不编译它。要跑时：

```bash
AGENTLOOM_TEST_REDIS_ADDR=127.0.0.1:16379 go test -tags redis -count=1 -run TestEmbedCacheRedisRoundTrip ./internal/infra/
```

没设置 `AGENTLOOM_TEST_REDIS_ADDR` 时该测试跳过。这次提交没有对真实 Redis 跑过这条测试。L1 未命中后回填 L2 的逻辑由默认测试里的内存假 L2 覆盖。
