#!/usr/bin/env bash
# WSL only: three-way retrieval comparison (keyword vs vector vs hybrid).
# This calls a real embedding API (corpus + per-query), so it costs some tokens.
set -euo pipefail
. "$(dirname "$0")/_common.sh"
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) printf '%s\n' 'Run this evaluation in WSL.' >&2; exit 1 ;;
esac
if [ -z "${EMBEDDING_BASE_URL:-}" ] || [ -z "${EMBEDDING_API_KEY:-}" ] || [ -z "${EMBEDDING_MODEL:-}" ]; then
    printf '%s\n' 'Set EMBEDDING_BASE_URL / EMBEDDING_API_KEY / EMBEDDING_MODEL before running.' >&2
    exit 1
fi
split="${KNOWLEDGE_RAG_SPLIT:-dev}"
mkdir -p test/rag/results
export KNOWLEDGE_RAG_SPLIT="$split"
export KNOWLEDGE_RAG_REPORT="$(pwd)/test/rag/results/compare-${split}-$(date -u +%Y%m%dT%H%M%SZ)-$$.json"
go test ./test/knowledge -tags knowledgeintegration,rageval -run '^TestRAGRetrievalComparison$' -count=1 -v
