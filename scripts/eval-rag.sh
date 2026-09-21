#!/usr/bin/env bash
# WSL only: isolated MySQL evaluation, no real model calls.
set -euo pipefail
. "$(dirname "$0")/_common.sh"
case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*) printf '%s\n' 'Run this evaluation in WSL.' >&2; exit 1 ;;
esac
mkdir -p test/rag/results
export KNOWLEDGE_RAG_REPORT="$(pwd)/test/rag/results/dev-$(date -u +%Y%m%dT%H%M%SZ)-$$.json"
go test ./test/knowledge -tags knowledgeintegration,rageval -run '^TestRAGDevelopmentEvaluation$' -count=1 -v
