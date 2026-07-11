# Changelog

All notable changes to OpenLinker Agent Node will be documented in this file.

This project is currently pre-1.0. Breaking changes may happen before the
runtime protocol, adapter interfaces, and CLI behavior are declared stable.

## Unreleased

### Breaking

- Replaced the earlier runtime transports with one production path: reliable
  Runtime v2 over HTTP long-polling.
- Runtime startup now requires a Core v2 URL, Node UUID, Agent UUID, persistent
  data directory, Agent Token, and TLS 1.3 mTLS client cert/key/CA files.
- Removed the legacy transport configuration, implementations, tests, and
  documentation; pre-v2 runtime behavior is not retained.
- Delegated Agent calls now require an explicit idempotency key. The same key
  represents a retry of one intent; a distinct intent needs a distinct key.

### Runtime reliability

- Added a single-process durable store with a stable worker ID, rotating
  runtime session/epoch, encrypted assignment capabilities, WAL/snapshots, and
  encrypted Event/Result spools.
- Persisted `received` and `ack_sent` before network acknowledgement, and gated
  adapter execution on typed assignment confirmation or an authoritative
  resume decision.
- Added stable per-Attempt Event sequences and IDs, stable Result IDs, typed ACK
  validation, ordered replay, exponential retry with jitter, and 4 MiB message
  enforcement.
- Added resume inventory/decisions, lease renewal and fencing, exact-Attempt
  cancellation, graceful capacity-zero shutdown, runtime session close, and
  process-tree termination for command/Codex adapters.
- Bound delegated calls to assignment-scoped node envelopes and short-lived
  invocation tokens. Long-lived Agent Tokens remain inside Agent Node.
- Vendored `openlinker-go` commit
  `2d566fd64ee9ed97c66be0ed92d1b0048f83d56c`, which removes the pre-v2 Go
  runtime API.

### Verification

- Added crash/restart, ACK-loss replay, no-double-execution, cancellation,
  stale-lease, corruption, encryption, message-boundary, delegated-call
  idempotency, process-tree, and TLS 1.3 mTLS/invocation-proof tests.
- Rewrote the English and Chinese runtime documentation for the v2-only
  configuration and recovery model.
