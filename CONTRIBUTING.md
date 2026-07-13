# Contributing to OpenLinker Agent Node

Chinese documentation: [CONTRIBUTING.zh-CN.md](./CONTRIBUTING.zh-CN.md)

Thanks for helping improve OpenLinker Agent Node. This repository owns the
local runtime process for Agents that connect to OpenLinker Core from private,
local, or NAT environments.

## Development Setup

```bash
go test ./...
go build ./cmd/openlinker-agent-node
```

Use placeholder Agent and helper tokens in tests and docs. Never commit a real
token, mTLS private key, invocation capability, private endpoint, local `.env`,
customer payload, or adapter log containing sensitive data.

## Scope Boundaries

Allowed here:

- Runtime WebSocket and HTTPS long-poll, assignment confirmation, lease, command, and resume behavior
- durable assignment, Event, and Result journal/spool behavior
- local HTTP, command, A2A, Codex, and similar adapter execution
- localhost helper behavior for delegation and progress events
- public A2A server behavior exposed by Agent Node
- CLI/runtime configuration and safety checks

Out of scope:

- Core registry storage, billing, marketplace ranking, and Cloud dashboards
- hosted payment, wallet, or withdrawal behavior
- backend-specific business logic for a particular Agent

## Runtime Rules

- Treat Agent Tokens, mTLS keys, invocation capabilities, spool keys, and helper tokens as secrets.
- Do not pass the Agent Token or invocation capability to backend subprocesses.
- Persist an assignment before ACK and execute only after Core confirmation.
- Reuse stable Event/Result IDs until the matching typed ACK arrives.
- Never rerun a previously started Attempt after a process crash without a durable per-process checkpoint.
- Require an explicit idempotency key for delegated calls: reuse it for the same intent, change it for a new intent.
- Keep adapters isolated from protocol internals where possible.
- Keep the Codex adapter in an isolated workspace.

## Pull Request Expectations

- Include tests for runtime transport, durability, adapter, helper, or public A2A behavior changes.
- Document new environment variables in `README.md`.
- Explain compatibility impact for existing runtimes.
- Redact tokens and local paths from logs or fixtures.

## Checks

```bash
gofmt -w .
go test ./...
go build ./cmd/openlinker-agent-node
```

## Security

Do not open public issues for vulnerabilities. Follow [SECURITY.md](./SECURITY.md).

## License

By contributing, you agree that your contribution is licensed under the
Apache-2.0 license used by this repository.
