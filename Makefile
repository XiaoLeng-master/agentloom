.PHONY: run bench eval test verify fmt vet tidy docker-up kafka-demo

GO ?= go

run:        ## 启动 HTTP 入口（agentd, :8080）
	$(GO) run ./cmd/agentd

bench:      ## 跑切分策略 benchmark，输出 HitRate@K 表
	$(GO) run ./cmd/bench

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

docker-up:  ## 起 Redis。Kafka 在 profile kafka，见 make kafka-demo
	docker compose up -d

kafka-demo: ## 本机起 Kafka 并让 agentd 消费一波 Trace
	bash scripts/kafka-demo.sh
