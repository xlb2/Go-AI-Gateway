#!/usr/bin/env sh
# Build to a stable path before starting the interactive Harness client.
set -e
. "$(dirname "$0")/_common.sh"
mkdir -p bin
go build -o "bin/gwagent$EXE" ./cmd/agent
exec "./bin/gwagent$EXE" "$@"
