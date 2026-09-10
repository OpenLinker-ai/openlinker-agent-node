# Changelog

All notable changes to OpenLinker Agent Node will be documented in this file.

This project is currently pre-1.0. Breaking changes may happen before the
runtime protocol, adapter interfaces, and CLI behavior are declared stable.

## Unreleased

### Local Claude bridge candidate — test prereleases only

- Build-injected Node identity (`dev`, exact release tag, or commit) and a
  read-only `--version` command that bypasses all runtime startup. Unsupported
  command arguments now fail instead of being ignored. This changes the
  enrollment version from the old hardcoded `openlinker-agent-node/0.1.43`.
  **Existing enrollment cannot be upgraded in place.** Test deployments without
  real users use a new NodeID, new unbound credential and new SDK DataDir after
  the old process has stopped with settled Attempts and empty spool; retain the
  old state. Ordinary active-Node restart keeps the same exact version and
  DataDir. No automatic rollback or general migration controller is supplied.
- The release gate now permits only canonical `v0.x.y-alpha.N`, `v0.x.y-beta.N`
  and `v0.x.y-rc.N` test tags, without an environment bypass. Stable/v1+ tags,
  malformed input and missing arguments fail. GitHub releases are marked
  prerelease; non-tag SHA artifacts remain CI-only. This replaces the blanket
  Core-upgrade prerequisite for fresh test enrollment, not Core's identity
  validation. WebSocket/pull intake and restart still need real verification.
- Long-lived Claude success diagnostics `claude_resume_session_id_sha256` and
  `claude_session_id_sha256`, bound to actual invocation/result IDs, with no raw
  IDs and no failed-retry evidence carried into a successful fresh invocation.
- Native Codex/Claude adapters and reusable protocol leaves belong to this
  repository, with no CLI/Plugin module dependency. Session reuse remains
  false by default; deep Plugin/Browser policy is unchanged.

### Breaking

- Removed Agent Node's duplicate Runtime client, transport supervisor,
  assignment journal, encrypted spool, and delivery state machine. There is no
  compatibility layer because the project is still pre-1.0.
- The pinned `openlinker-go` SDK now owns the complete reliable Runtime Worker.
  Public Go names are generation-free `Runtime*`, and Runtime URLs do not carry
  a protocol generation.
- Normal startup uses `OPENLINKER_URL` to discover the Runtime origin and its
  token-only or mTLS policy from `/.well-known/openlinker.json`.
  `OPENLINKER_RUNTIME_URL` remains an advanced address override but no longer
  bypasses discovery or downgrades its security policy.
- Delegated Agent calls require an explicit idempotency key. Reuse a key only
  for retries of the same intent.
- The optional public A2A listener no longer executes an Adapter or owns
  in-memory task and push-notification state. It retains only local Agent Card,
  bearer-authentication, request-size, and timeout guards; the Go SDK proxies
  every stateful A2A operation to Core over Agent Token plus the discovered security policy. REST and
  JSON-RPC Agent Card responses remain stateless and local so their external
  URL continues to identify the AgentNode listener.

### SDK boundary

- Token-only startup no longer requires `OPENLINKER_NODE_ID`; the pinned SDK
  derives a deterministic token-scoped identity. Explicit mTLS startup keeps
  the provisioned Node ID requirement.
- Pinned `openlinker-go` version `v0.2.0-rc8.0.20260908135527-31afbf9c1a18`;
  dependencies use standard Go module resolution instead of a checked-in vendor tree.
- The SDK owns discovery, token-only/TLS 1.3 mTLS policy, Session identity, WebSocket/Pull
  switching, assignment confirmation, lease renewal, resume, cancellation,
  drain, durable assignment state, encrypted Event/Result delivery, ACK repair,
  backpressure, and duplicate-execution prevention.
- Agent Node now owns only CLI and environment parsing, Adapter selection,
  HTTP/command/Codex/A2A execution, localhost helper sessions, process-tree
  control, the public A2A listener shell, and SDK file-store directory
  selection. Core owns public A2A message, task, run, stream, and push state.
- Prior builds identify themselves to Core as `openlinker-agent-node/0.1.43`;
  the version-injection candidate above is limited to fresh test enrollments. Direct SDK
  workers default to `openlinker-go/runtime-worker`.

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
- Added token-only and mTLS proxy regressions for public A2A operation families, SSE,
  identity/header isolation, oversized requests, unavailable Core, and
  listener generation-safe shutdown/restart.
- Verified both repositories with ordinary and race-enabled Go tests and a
  read-only standard module dependency graph.
