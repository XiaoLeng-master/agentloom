# 09 · 可选 Kafka Trace 管道

未设置 `AGENTLOOM_KAFKA_BROKERS` 时，agentd 保持原来的路径：Trace 先写入进程内存；若设置了 `AGENTLOOM_REDIS_ADDR`，再经内存 `ToyQueue` 异步写入 Redis。设置了 broker 地址后，这一次进程不再启动 Redis 队列，Chat 产生的 Trace 进入 Kafka。

客户端是纯 Go 的 `github.com/segmentio/kafka-go`。消费端按 `trace_id` 做 `INSERT OR IGNORE`，同一条重复投递不会再插一行，`MonthlySharder` 也只在首次插入时记账。月份表名是 `traces_YYYY_MM`。处理失败会按 `AGENTLOOM_KAFKA_ATTEMPTS`（默认 3）重试，耗尽后写入死信主题。死信也写失败时不提交 offset，下一次再试。

本文件不记录任何未在对应机器上跑出来的计数。编写时的云端环境没有 `docker` 命令，因此没有对真实 broker 跑过集成测试，也没有跑过下面的演示。

## Mac 上一条命令

需要 Docker Desktop（或同等的 Docker Compose v2）和 Go 1.22+。在仓库根目录：

```bash
make kafka-demo
```

这条命令会：

1. `docker compose --profile kafka up -d --wait kafka`，镜像 `apache/kafka:3.9.0`，单节点 KRaft，只把 `127.0.0.1:9092` 暴露给本机。不加 profile 时 `make docker-up` 仍然只起 Redis。
2. 用当次时间戳生成独立的 topic、死信 topic 和消费组，避免和上一次演示混在一起。
3. 在 `127.0.0.1:8080` 启动 `agentd`。`AGENTLOOM_TRACE_HANDLE_DELAY` 默认 `20ms`，让消费慢于写入，便于看到堆积。SQLite 文件在 `.agentloom/`（已 gitignore）。
4. `cmd/kafkademo` 向 `POST /v1/trace-pipeline` 连续写入 200 条 Trace（每 20 条里有 1 条落在 2026-11，其余在 2026-10），轮询 `GET /v1/trace-pipeline`，再写入一条非法 JSON，等死信计数增加。

标准输出是当次 JSON，字段都来自这次进程读到的计数，例如：

- `max_buffered`：本进程发往 broker 前的内存缓冲峰值
- `max_reader_lag`：kafka-go reader 报告的 lag 峰值
- `max_backlog_estimate`：本进程 `published - consumed` 的峰值（已发布但尚未处理完，含随后进入死信的消息）
- `final.consumed`、`final.dlq`、`final.stored`、`final.months`：结束时的消费、死信、入库行数和月份
- `consumed_per_sec`：`final.consumed / (wall_ms/1000)`
- `wall_ms`、`brokers`、`topic`

把这次 JSON 原样保留即可。不要把某一次机器上的数字抄到别的环境里当成固定结果。

可选环境变量：

| 变量 | 作用 |
|---|---|
| `AGENTLOOM_KAFKA_BROKERS` | 逗号分隔的 broker。空则关闭 Kafka |
| `AGENTLOOM_KAFKA_TOPIC` | 主主题，默认 `agentloom.traces`。演示脚本会改成带时间戳的名字 |
| `AGENTLOOM_KAFKA_DLQ_TOPIC` | 死信主题，默认 `agentloom.traces.dlq` |
| `AGENTLOOM_KAFKA_GROUP` | 消费组，默认 `agentloom-traces` |
| `AGENTLOOM_KAFKA_ATTEMPTS` | 入库重试次数，默认 3 |
| `AGENTLOOM_TRACE_DB` | SQLite 路径，默认 `.agentloom/traces.db` |
| `AGENTLOOM_TRACE_HANDLE_DELAY` | 每条入库前的等待，默认 0。演示脚本用 `20ms` |
| `KAFKA_DEMO_N` | 演示条数，默认 200 |
| `AGENTLOOM_DEMO_ADDR` | agentd 监听地址，默认 `127.0.0.1:8080` |

重放死信（需要 broker 已在跑，且 agentd 仍在消费同一个主主题）：

```bash
AGENTLOOM_KAFKA_BROKERS=localhost:9092 \
AGENTLOOM_KAFKA_TOPIC=刚才的主主题 \
AGENTLOOM_KAFKA_DLQ_TOPIC=刚才的死信主题 \
go run ./cmd/kafkademo -replay
```

没有 `id` 的死信再投一次仍会失败并再次进入死信。已有 `trace_id` 的记录重放后不会多出一行。

## 集成测试

`internal/infra/kafka_integration_test.go` 带 `//go:build integration`。没有 Docker **并且** 没有 `AGENTLOOM_KAFKA_BROKERS` 时跳过；只设置了 brokers 也会连那个地址。普通测试不包含这个文件：

```bash
go test ./...
go test -tags integration ./internal/infra/
```

有 Docker 时，先 `make kafka-demo` 把 broker 留在 `localhost:9092`，再另开终端：

```bash
AGENTLOOM_KAFKA_BROKERS=localhost:9092 go test -tags integration -count=1 -timeout 60s ./internal/infra/ -run TestKafkaIdempotentMonthlyAndDLQ
```

该测试写入两条相同的 2026-10 记录、一条 2026-12 记录和一条非法 JSON，期望 10 月与 12 月各一行、死信至少 1。
