# Changelog

All notable changes to OpenLinker Agent Node will be documented in this file.

This project is currently pre-1.0. Breaking changes may happen before the
runtime protocol, adapter interfaces, and CLI behavior are declared stable.

## Unreleased

### Breaking

- Replaced every legacy transport with the current reliable Runtime. WebSocket
  is the default and HTTPS long-poll is the restricted-network fallback.
- Normal startup now takes one `OPENLINKER_URL` and discovers the dedicated
  mTLS Runtime origin from `/.well-known/openlinker.json`. The old Core URL
  setting is no longer read, and the Runtime endpoints no longer include a
  version segment.
- Runtime startup requires an OpenLinker address, Node UUID, Agent UUID,
  persistent data directory, Agent Token, and TLS 1.3 mTLS client cert/key/CA
  files. `OPENLINKER_RUNTIME_URL` remains an advanced explicit override.
- Removed the legacy transport configuration, implementations, tests, and
  documentation; the old behavior is not retained.
- Delegated Agent calls now require an explicit idempotency key. The same key
  represents a retry of one intent; a distinct intent needs a distinct key.

### Runtime reliability

- Added `OPENLINKER_AGENT_NODE_TRANSPORT=auto|ws|pull`. `auto` starts with
  WebSocket, switches to HTTPS long-poll after an unavailable or disconnected socket,
  and probes with exponential backoff before returning to WebSocket.
- Added an explicit transport state machine that cancels and drains the old
  generation, detaches it, attaches and resumes the same durable identity, and
  publishes the replacement only after recovery. WS and Pull cannot claim
  concurrently, and duplicate assignments cannot rerun a started adapter.

- Added a single-process durable store with a stable worker ID, rotating
  runtime session/epoch, encrypted assignment capabilities, WAL/snapshots, and
  encrypted Event/Result spools.
- Enforced a 512 MiB / 10,000-record spool envelope, an 80% new-Run admission
  gate, and a 16 MiB logical and filesystem control reserve. Existing uploads,
  cancellation, and cleanup remain available under backpressure.
- Persisted `received` and `ack_sent` before network acknowledgement, and gated
  adapter execution on typed assignment confirmation or an authoritative
  resume decision.
- Added stable per-Attempt Event sequences and IDs, stable Result IDs, typed ACK
  validation, ordered replay, exponential retry with jitter, and 4 MiB message
  enforcement.
- Retained ACKed Event records until Result ACK and added exact-range replay for
  `EVENTS_MISSING` before retrying the same stable Result.
- Added resume inventory/decisions, lease renewal and fencing, exact-Attempt
  cancellation, graceful capacity-zero shutdown, runtime session close, and
  process-tree termination for command/Codex adapters.
- Bound delegated calls to assignment-scoped node envelopes and short-lived
  invocation tokens. Long-lived Agent Tokens remain inside Agent Node.
- Vendored `openlinker-go` commit
  `2ab750706b7ed7f6744a6d360e04604add112fe2`, including the complete
  canonical Runtime HTTP contract and its session heartbeat/close semantics.

### Verification

- Added crash/restart, ACK-loss replay, no-double-execution, cancellation,
  stale-lease, corruption, encryption, message-boundary, delegated-call
  idempotency, process-tree, TLS 1.3 mTLS/invocation-proof, confirm-before-run,
  WS-to-Pull-to-WS, Core replacement, and cross-transport claim exclusion tests.
- Added crash injection at pre-write, post-fsync, post-rename, post-directory
  fsync, pre-WAL, post-WAL, send/ACK-loss, and Result-ACK cleanup boundaries.
- Rewrote the English and Chinese Runtime documentation for automatic
  discovery, transport fallback, and the recovery model.
