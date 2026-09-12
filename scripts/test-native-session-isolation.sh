#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(id -u)" == 0 ]]; then
  echo 'Native isolation acceptance must run as a non-root user.' >&2
  exit 1
fi
: "${OPENLINKER_TEST_NATIVE_SANDBOX_BIN:?Set this to the installed pinned srt executable (no automatic install or fallback).}"
export GOWORK=off
node --test scripts/native-sandbox-runner.test.mjs
go test -race -count=1 ./pkg/adapters/sessionsandbox ./pkg/adapters ./internal/agentnode
