.PHONY: run bench eval test fmt vet tidy docker-up

GO ?= go

run:        ## 启动 HTTP 入口（WIP）
	$(GO) run ./cmd

bench:      ## 跑切分策略 benchmark，输出 HitRate@K 表
	bash scripts/bench.sh

eval:       ## 跑评测 harness，产出 report.md / diagnosis.md
	bash scripts/eval.sh

test:       ## 全量测试 + race
	$(GO) test -race ./...

fmt:        ## 格式化
	$(GO) fmt ./...

vet:        ## 静态检查
	$(GO) vet ./...

tidy:       ## 整理依赖
	$(GO) mod tidy

docker-up:  ## 起依赖（etcd/redis/milvus/kafka）
	docker compose up -d
