#!/usr/bin/env bash
# Explicit real-model evaluation, one fixed case per invocation. WSL only.
set -euo pipefail
. "$(dirname "$0")/_common.sh"
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) printf '%s\n' 'Run this evaluation in WSL.' >&2; exit 1 ;;
esac
case "${1:-}" in
  --real) shift ;;
  *) printf '%s\n' 'Usage: bash scripts/context-baseline.sh --real [case-id] [--holdout] [--apply-shadow]' >&2; exit 2 ;;
esac
export CONTEXT_BASELINE_HOLDOUT=0
export CONTEXT_BASELINE_APPLY_SHADOW=0
case_id=continuity-dev
case_set=0
while (($#)); do
  case "$1" in
    --holdout) CONTEXT_BASELINE_HOLDOUT=1 ;;
    --apply-shadow) CONTEXT_BASELINE_APPLY_SHADOW=1 ;;
    --*) printf '%s\n' "Unexpected option: $1" >&2; exit 2 ;;
    *)
      if ((case_set)); then printf '%s\n' 'Only one case-id is allowed.' >&2; exit 2; fi
      case_id="$1"
      case_set=1
      ;;
  esac
  shift
done
export CONTEXT_BASELINE_CASE="$case_id"
if [[ "$CONTEXT_BASELINE_APPLY_SHADOW" == 1 ]]; then
  budget="${CONTEXT_BASELINE_SHADOW_BUDGET:-}"
  if [[ ! "$budget" =~ ^[0-9]+$ || ! "$budget" =~ [1-9] ]]; then
    printf '%s\n' '--apply-shadow requires CONTEXT_BASELINE_SHADOW_BUDGET to be a positive integer.' >&2
    exit 2
  fi
fi
mkdir -p test/contextbaseline/results
export CONTEXT_BASELINE_REPORT="$(pwd)/test/contextbaseline/results/run-$(date -u +%Y%m%dT%H%M%SZ)-$$.json"
export CONTEXT_BASELINE_REAL=1
go test ./test/contextbaseline -tags contexteval -run '^TestContextBaselineReal$' -count=1 -timeout 15m -v
