#!/usr/bin/env bash
# Starts the compose Kafka profile and one local agentd, then publishes a burst.
# Numbers are printed by cmd/kafkademo from that run. This script does not invent them.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

if ! command -v docker >/dev/null 2>&1; then
  echo "需要本机 Docker 才能执行 make kafka-demo（当前环境没有 docker 命令）" >&2
  exit 1
fi

docker compose --profile kafka up -d --wait kafka

stamp="$(date -u +%Y%m%d%H%M%S)"
export AGENTLOOM_KAFKA_BROKERS="${AGENTLOOM_KAFKA_BROKERS:-localhost:9092}"
export AGENTLOOM_KAFKA_TOPIC="agentloom.demo.${stamp}"
export AGENTLOOM_KAFKA_DLQ_TOPIC="${AGENTLOOM_KAFKA_TOPIC}.dlq"
export AGENTLOOM_KAFKA_GROUP="${AGENTLOOM_KAFKA_TOPIC}"
export AGENTLOOM_TRACE_HANDLE_DELAY="${AGENTLOOM_TRACE_HANDLE_DELAY:-20ms}"
export AGENTLOOM_KAFKA_ATTEMPTS="${AGENTLOOM_KAFKA_ATTEMPTS:-3}"
export AGENTLOOM_TRACE_DB="${AGENTLOOM_TRACE_DB:-.agentloom/kafka-demo-${stamp}.db}"

addr="${AGENTLOOM_DEMO_ADDR:-127.0.0.1:8080}"
go run ./cmd/agentd -addr "$addr" &
pid=$!
cleanup() {
  kill "$pid" >/dev/null 2>&1 || true
  wait "$pid" >/dev/null 2>&1 || true
}
trap cleanup EXIT

go run ./cmd/kafkademo -url "http://${addr}" -n "${KAFKA_DEMO_N:-200}"
