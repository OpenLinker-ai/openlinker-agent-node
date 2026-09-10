# Codex stdio protocol

This is the shared transport used by Plugin `agentexec`, the CLI Worker and
Agent Node. Each invocation owns one `codex app-server --listen stdio://`
process. There is no resident server, exec fallback, or Agent Node copy.

`schema/protocol.json` contains the transitively closed stable schema subset
exported by Codex **0.153.0**, source commit
`41e22fee981a63b3698df7ed36bad393cda24715`. `protocol.gen.go` is generated from
that file; its header includes the schema SHA-256. Tagged item/input envelopes
retain conflicting variant fields as `json.RawMessage`; consumers must inspect
`type` before using a variant. This is a binding generator, not a JSON Schema
validator. Unknown unions stay raw rather than silently losing variants.

Refresh with the pinned CLI on PATH (or set `CODEX_BIN` for the version check):

```sh
codex app-server generate-json-schema --out /tmp/codex-schema-0.153.0
node scripts/generate-codex-rpc.mjs /tmp/codex-schema-0.153.0
node scripts/generate-codex-rpc.mjs --check
```

Regeneration from the vendored schema needs only Node and Go; CI runs `--check`
without a provider binary. Changing versions requires updating the generator,
compatibility baseline, image pin/integrity and protocol acceptance tests.

The client bounds each incoming JSONL message at 4 MiB, pending calls and
queues at 128, without a cumulative transcript cap. Initialization opts out of
unused agent-message and reasoning deltas. A full notification queue fails
immediately rather than blocking response routing behind a pending call.
The client correlates request IDs and responds to server requests while a
client request is pending. Approval/elicitation requests are declined; dynamic
tools and credential-refresh callbacks are unsupported. Unknown notifications
cannot select a thread, supply an answer, or authorize a callback. Process
termination closes both I/O pumps, including an unresponsive writer.

The official `0.153.0` implementation reports missing rollouts as JSON-RPC
`-32600` plus a message, without a dedicated structured missing-thread code.
Recovery is therefore limited to `thread/resume` and its exact known diagnostic,
never all `-32600` errors. Upgrade the fixture if upstream adds a stable code.

An opt-in acceptance test runs the installed real CLI against an in-process
fake Responses API and a local MCP fixture, with disposable homes and fake
credentials. It covers native session resume, final-answer extraction, config
isolation and native Browser plugin activation without a real model call.
It covers both direct MCP calls and `exec` discovery/calls with a catalog model
that requires Code Mode. The latter checks that shell tools are absent and
`process`, `require`, `Deno`, `fetch`, `XMLHttpRequest` and `WebSocket` are
undefined. These are regression sentinels for selected APIs, not an exhaustive
audit of dynamic imports or handles reachable through `globalThis`, nor proof
of a V8 sandbox. Isolation in the official deployment relies on the hardened
container boundary, read-only sandbox, disabled shell/multi-agent tools and
Browser/delegation authorization. CI installs the image-pinned Codex and runs
this test without provider secrets:

```sh
OPENLINKER_TEST_CODEX_RPC_LOCAL_MODEL=1 go test ./packages/agent-adapters/agentexec \
  -run TestInstalledCodexRPCWithLocalResponsesAPI -v
```

Native authentication refresh is independently tested against a local token
endpoint with synthetic nearly-expired tokens. The pinned CLI writes auth in
place: the source mtime/token change, the Attempt symlink remains, and cleanup
preserves the refreshed source file. Re-run this test on provider upgrades;
an upstream switch to rename-based persistence would invalidate the contract.

```sh
OPENLINKER_TEST_CODEX_AUTH_REFRESH=1 go test ./packages/agent-adapters/codexhome \
  -run TestInstalledCodexRefreshPersistsThroughAuthSymlink -v
```

References: [app-server protocol](https://learn.chatgpt.com/docs/app-server),
[pinned upstream source](https://github.com/openai/codex/tree/41e22fee981a63b3698df7ed36bad393cda24715/codex-rs/app-server).
The upstream schema is Apache-2.0 licensed; see `schema/LICENSE`.
