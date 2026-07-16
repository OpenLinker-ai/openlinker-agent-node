# Changelog

All notable changes to OpenLinker Agent Node will be documented in this file.

This project is currently pre-1.0. Breaking changes may happen before the
runtime protocol, adapter interfaces, and CLI behavior are declared stable.

## Unreleased

### Breaking

- Removed Agent Node's duplicate Runtime client, transport supervisor,
  assignment journal, encrypted spool, and delivery state machine. There is no
  compatibility layer because the project is still pre-1.0.
- The pinned `openlinker-go` SDK now owns the complete reliable Runtime Worker.
  Public Go names are generation-free `Runtime*`, and Runtime URLs do not carry
  a protocol generation.
- Normal startup uses `OPENLINKER_URL` to discover the dedicated mTLS Runtime
  origin from `/.well-known/openlinker.json`. `OPENLINKER_RUNTIME_URL` remains
  an advanced HTTPS override.
- Delegated Agent calls require an explicit idempotency key. Reuse a key only
  for retries of the same intent.
- The optional public A2A listener no longer executes an Adapter or owns
  in-memory task and push-notification state. It retains only local Agent Card,
  bearer-authentication, request-size, and timeout guards; the Go SDK proxies
  every stateful A2A operation to Core over Agent Token plus mTLS. REST and
  JSON-RPC Agent Card responses remain stateless and local so their external
  URL continues to identify the AgentNode listener.

### SDK boundary

- Pinned and vendored `openlinker-go` commit
  `4ce0eec90a533e235cb28e2045f136529186bf51`.
- The SDK owns discovery, TLS 1.3 mTLS, Session identity, WebSocket/Pull
  switching, assignment confirmation, lease renewal, resume, cancellation,
  drain, durable assignment state, encrypted Event/Result delivery, ACK repair,
  backpressure, and duplicate-execution prevention.
- Agent Node now owns only CLI and environment parsing, Adapter selection,
  HTTP/command/Codex/A2A execution, localhost helper sessions, process-tree
  control, the public A2A listener shell, and SDK file-store directory
  selection. Core owns public A2A message, task, run, stream, and push state.
- Agent Node identifies itself to Core as `openlinker-agent-node/0.1.43`; direct
  SDK workers default to `openlinker-go/runtime-worker`.

### Verification

- Migrated Runtime failure-matrix and durable-store conformance coverage into
  the Go SDK, including crash/restart, ACK loss, cancellation, lease fencing,
  corruption, backpressure, and WebSocket/Pull recovery.
- Added static Agent Node boundary tests that reject reintroduced Runtime
  state-machine files, local public A2A authority, and direct Runtime wire
  operations, and require `NewRuntimeWorker` plus `NewRuntimeA2AProxy`
  integration.
- Added regressions for Stop during Worker startup and for Adapter error,
  cancellation, and panic mapping.
- Added mTLS proxy regressions for all public A2A operation families, SSE,
  identity/header isolation, oversized requests, unavailable Core, and
  listener generation-safe shutdown/restart.
- Verified both repositories with ordinary and race-enabled Go tests, and
  verified Agent Node from the vendored dependency graph.
