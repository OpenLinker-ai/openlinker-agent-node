#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(id -u)" == 0 ]]; then
  echo 'Native isolation acceptance must run as a non-root user.' >&2
  exit 1
fi
: "${OPENLINKER_TEST_NATIVE_CODEX_BIN:?Set the pinned official Codex executable.}"
: "${OPENLINKER_TEST_NATIVE_CLAUDE_BIN:?Set the pinned official Claude executable.}"
export GOWORK=off
node --test scripts/native-sandbox-runner.test.mjs
go test -race -count=1 -v ./pkg/adapters/sessionsandbox ./pkg/adapters ./internal/agentnode
