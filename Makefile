.PHONY: run bench loop canary eval test verify fmt vet tidy docker-up

GO ?= go

run:        ## 启动 HTTP 入口（agentd, :8080）
	$(GO) run ./cmd/agentd

bench:      ## 跑切分策略 benchmark，输出 HitRate@K 表
	$(GO) run ./cmd/bench

loop:       ## 评测—归因—优化—复测门禁（确定性本地替身，98 条）
	$(GO) run ./cmd/loop

canary:     ## 进程内灰度放量与回滚（命令给定窗口，不是公开集实测）
	$(GO) run ./cmd/canary

verify:     ## 一键自证：格式化 + 静态检查 + 全量测试（含 race）
	$(GO) fmt ./...
	$(GO) vet ./...
	$(GO) test -race ./...

fmt:        ## 格式化
	$(GO) fmt ./...

vet:        ## 静态检查
	$(GO) vet ./...

tidy:       ## 整理依赖
	$(GO) mod tidy

docker-up:  ## 起依赖（etcd/redis/milvus/kafka）
	docker compose up -d
