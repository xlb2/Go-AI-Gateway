#!/usr/bin/env bash
# Explicit real-model evaluation, one fixed case per invocation. WSL only.
set -euo pipefail
. "$(dirname "$0")/_common.sh"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) printf '%s\n' 'Run this evaluation in WSL.' >&2; exit 1 ;;
esac
case "${1:-}" in
  --real) shift ;;
  *) printf '%s\n' 'Usage: bash scripts/context-baseline.sh --real [case-id] [--holdout]' >&2; exit 2 ;;
esac
export CONTEXT_BASELINE_CASE="${1:-continuity-dev}"
if (($#)); then shift; fi
export CONTEXT_BASELINE_HOLDOUT=0
if [[ "${1:-}" == '--holdout' ]]; then CONTEXT_BASELINE_HOLDOUT=1; shift; fi
if (($#)); then printf '%s\n' 'Unexpected arguments.' >&2; exit 2; fi
mkdir -p test/contextbaseline/results
export CONTEXT_BASELINE_REPORT="$(pwd)/test/contextbaseline/results/run-$(date -u +%Y%m%dT%H%M%SZ)-$$.json"
export CONTEXT_BASELINE_REAL=1
go test ./test/contextbaseline -tags contexteval -run '^TestContextBaselineReal$' -count=1 -timeout 15m -v
