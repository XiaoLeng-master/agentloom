# 可选 etcd 热更新

未设置 `AGENTLOOM_ETCD_ENDPOINTS` 时，`agentd` 使用进程内内存配置，`make run` 不连接 etcd。设置端点后，`EtcdSource` 实现现有 `Source` 接口：稳定配置文档走 `Manager.Reload` 和 `Drain`；灰度文档调用已有的 `StartCanary`、`SetCanaryPercent`、`PromoteCanary` 和 `RollbackCanary`，不另写分流。

键前缀默认 `/agentloom/agents/`，可用 `AGENTLOOM_ETCD_PREFIX` 覆盖。

| 键 | 作用 |
|---|---|
| `/agentloom/agents/kb-agent/config` | 稳定配置。新请求看到新版本，已拿到句柄的请求保持原配置，直到引用计数排空。 |
| `/agentloom/agents/kb-agent/canary` | 按 `percent` 做租户哈希分流。`percent` 为 100 时把候选升成稳定配置。 |
| 删除上面的 canary 键，或写入 `{"rollback":true}` | 一键回滚。新请求回到稳定配置，已拿到灰度配置的请求保持该配置。 |

`percent` 是 100 个桶里的前 N 个。租户 `tenant-79` 落在桶 0，10% 时进入灰度。`tenant-74` 落在桶 99，10% 和 50% 都留在稳定配置。`tenant-8` 落在桶 40，10% 时留在稳定配置，50% 时进入灰度。不带 `X-Tenant` 的请求按租户 `local` 计算，`local` 落在桶 84，因此 10% 和 50% 时仍读稳定配置。

下面的版本号来自单元测试 `TestEtcdDocumentsDriveReloadCanaryAndRollback` 对这几份 JSON 的结果：内存注册是版本 1；第一份稳定文档把新请求切到版本 2；灰度文档是版本 3；删除灰度键后新请求回到版本 2；100% 再把版本 3 升成稳定配置。本文不写耗时。

## 本机一条命令

环境：Mac 上的 Docker Desktop，以及 Go 1.22。仓库根目录：

```bash
make etcd-demo
```

这条命令执行 `docker compose -f deploy/docker-compose.yml up -d --wait etcd`，镜像是 `quay.io/coreos/etcd:v3.5.16`，只发布 `127.0.0.1:2379`，然后以前台方式启动 `agentd`。日志里会出现 `config source: etcd endpoints=127.0.0.1:2379 prefix=/agentloom/agents/`。

另开一个终端做下面的检查。`etcdctl` 在容器里，`ETCDCTL_API=3`。

启动后、写入 etcd 之前：

```bash
curl -s localhost:8080/v1/config
```

预期字段：`version` 为 1，`system_prompt` 为 `You are a knowledge-base assistant.`，`chunk_strategy` 为 `fixed`，`canary_active` 为 false。

热更新稳定配置：

```bash
docker compose -f deploy/docker-compose.yml exec etcd \
  etcdctl --endpoints=http://127.0.0.1:2379 put /agentloom/agents/kb-agent/config \
  '{"system_prompt":"etcd-stable","model":"qwen2.5","max_iter":10,"chunk_strategy":"recursive","prompt_mode":"echo","top_k":5}'
curl -s localhost:8080/v1/config
```

`etcdctl` 预期输出 `OK`。`curl` 预期：`version` 为 2，`system_prompt` 为 `etcd-stable`，`chunk_strategy` 为 `recursive`，`prompt_mode` 为 `echo`，`top_k` 为 5，`canary_active` 为 false。`agentd` 日志出现 `kind=config`、`version=2`。

10% 灰度：

```bash
docker compose -f deploy/docker-compose.yml exec etcd \
  etcdctl --endpoints=http://127.0.0.1:2379 put /agentloom/agents/kb-agent/canary \
  '{"percent":10,"system_prompt":"etcd-canary","model":"qwen2.5","max_iter":10,"chunk_strategy":"fixed","prompt_mode":"echo","top_k":3}'
curl -s -H 'X-Tenant: tenant-79' localhost:8080/v1/config
curl -s -H 'X-Tenant: tenant-74' localhost:8080/v1/config
curl -s localhost:8080/v1/config
```

`tenant-79` 预期：`version` 为 3，`system_prompt` 为 `etcd-canary`，`chunk_strategy` 为 `fixed`，`in_canary` 为 true，`canary_percent` 为 10。`tenant-74` 和默认的 `local` 预期：`version` 为 2，`system_prompt` 为 `etcd-stable`，`in_canary` 为 false。

放到 50%：

```bash
docker compose -f deploy/docker-compose.yml exec etcd \
  etcdctl --endpoints=http://127.0.0.1:2379 put /agentloom/agents/kb-agent/canary \
  '{"percent":50,"system_prompt":"etcd-canary","model":"qwen2.5","max_iter":10,"chunk_strategy":"fixed","prompt_mode":"echo","top_k":3}'
curl -s -H 'X-Tenant: tenant-79' localhost:8080/v1/config
curl -s -H 'X-Tenant: tenant-8' localhost:8080/v1/config
curl -s -H 'X-Tenant: tenant-74' localhost:8080/v1/config
```

`tenant-79` 仍是版本 3。`tenant-8` 此时进入灰度，版本 3。`tenant-74` 仍是版本 2 的稳定配置。

一键回滚：

```bash
docker compose -f deploy/docker-compose.yml exec etcd \
  etcdctl --endpoints=http://127.0.0.1:2379 del /agentloom/agents/kb-agent/canary
curl -s -H 'X-Tenant: tenant-79' localhost:8080/v1/config
```

`etcdctl` 预期输出 `1`。`curl` 预期回到版本 2、`etcd-stable`，`canary_active` 为 false。也可以不删除键，改为写入 `{"rollback":true}`，效果相同。

100% 放量会把候选升成唯一稳定配置，之后 `canary_active` 为 false，`Acquire` 和不带租户的请求都读到候选：

```bash
docker compose -f deploy/docker-compose.yml exec etcd \
  etcdctl --endpoints=http://127.0.0.1:2379 put /agentloom/agents/kb-agent/canary \
  '{"percent":100,"system_prompt":"etcd-canary","model":"qwen2.5","max_iter":10,"chunk_strategy":"fixed","prompt_mode":"echo","top_k":3}'
curl -s localhost:8080/v1/config
```

预期：`version` 为 3，`system_prompt` 为 `etcd-canary`，`canary_active` 为 false。

停止前台的 `agentd` 用 Ctrl-C。停止 etcd：

```bash
docker compose -f deploy/docker-compose.yml stop etcd
```

## 集成测试

普通 `go test ./...` 不连接 etcd。有 Docker 时，在仓库根目录：

```bash
make etcd-integration
```

它拉起同一个 compose 服务，再执行 `go test -tags integration -count=1 ./internal/config/ -run TestEtcdWatchReloadsAndRollsBack`。没有 Docker、也没有 `AGENTLOOM_ETCD_ENDPOINTS` 时，带 `integration` 标签的测试会跳过。
