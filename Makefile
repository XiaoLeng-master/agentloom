.PHONY: run bench loop canary etcd-demo etcd-integration eval test verify fmt vet tidy docker-up

GO ?= go

run:        ## 启动 HTTP 入口（agentd, :8080）
	$(GO) run ./cmd/agentd

bench:      ## 跑切分策略 benchmark，输出 HitRate@K 表
	$(GO) run ./cmd/bench

loop:       ## 评测—归因—优化—复测门禁（确定性本地替身，98 条）
	$(GO) run ./cmd/loop

canary:     ## 进程内灰度放量与回滚（命令给定窗口，不是公开集实测）
	$(GO) run ./cmd/canary

etcd-demo:  ## 本机起单节点 etcd 并前台启动 agentd（需要 Docker）
	docker compose -f deploy/docker-compose.yml up -d --wait etcd
	AGENTLOOM_ETCD_ENDPOINTS=127.0.0.1:2379 $(GO) run ./cmd/agentd

etcd-integration: ## 对本机 etcd 跑集成测试（需要 Docker；没有则不要用这条）
	docker compose -f deploy/docker-compose.yml up -d --wait etcd
	AGENTLOOM_ETCD_ENDPOINTS=127.0.0.1:2379 $(GO) test -tags integration -count=1 ./internal/config/ -run TestEtcdWatchReloadsAndRollsBack

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
