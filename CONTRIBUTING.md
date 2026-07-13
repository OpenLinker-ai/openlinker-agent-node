# Contributing to OpenLinker Agent Node

Chinese documentation: [CONTRIBUTING.zh-CN.md](./CONTRIBUTING.zh-CN.md)

Thanks for helping improve OpenLinker Agent Node. This repository provides a
thin Adapter process for HTTP, command, A2A, and Codex backends. The pinned
`openlinker-go` SDK owns the reliable Runtime Worker.

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

- local HTTP, command, A2A, Codex, and similar adapter execution
- localhost helper behavior for delegation and progress events
- public A2A server behavior exposed by Agent Node
- CLI and environment parsing, SDK configuration, process control, and file-store selection
- reproducible defects in the boundary between this host process and the pinned SDK

Out of scope:

- Core registry storage, billing, marketplace ranking, and Cloud dashboards
- hosted payment, wallet, or withdrawal behavior
- backend-specific business logic for a particular Agent
- Runtime discovery, mTLS, transport switching, Session state, journal/spool,
  lease, resume, cancellation, drain, and ACK repair; these belong in
  `openlinker-go`

## Runtime Rules

- Treat Agent Tokens, mTLS keys, invocation capabilities, SDK store keys, and helper tokens as secrets.
- Do not pass the Agent Token or invocation capability to backend subprocesses.
- Require an explicit idempotency key for delegated calls: reuse it for the same intent, change it for a new intent.
- Keep adapters isolated from Runtime protocol internals; do not add a second
  transport, journal, spool, or delivery state machine here.
- Keep the Codex adapter in an isolated workspace.

## Pull Request Expectations

- Include tests for Adapter, CLI, helper, process-control, SDK integration, or
  public A2A behavior changes.
- Document new environment variables in `README.md`.
- Explain compatibility impact for existing Adapter deployments.
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
