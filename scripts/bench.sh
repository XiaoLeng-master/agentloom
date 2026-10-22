#!/usr/bin/env bash
# Run the RAG chunking-strategy benchmark against datasets/rag_eval_80.json.
# Outputs a HitRate@K comparison table (fixed / recursive / semantic / parent-child)
# to stdout and docs/03-benchmark.md (append). D6 provides the Go bench command.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "=== agentloom RAG chunking benchmark ==="
echo "dataset: datasets/rag_eval_80.json ($(python3 -c "import json;print(len(json.load(open('datasets/rag_eval_80.json'))))") cases)"
echo

# D6 provides cmd/bench; fall back to go test -bench if the command is absent.
if [ -x "$(go env GOPATH)/bin/agentloom-bench" ] || go build -o /tmp/agentloom-bench ./cmd/bench 2>/dev/null; then
  /tmp/agentloom-bench
else
  echo "(cmd/bench not built yet; running internal/rag benchmarks)"
  go test -bench=. -benchmem ./internal/rag/
fi
