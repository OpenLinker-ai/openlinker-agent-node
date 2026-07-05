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

Use placeholder runtime tokens in tests and docs. Never commit a real token,
private endpoint, local `.env`, customer payload, or adapter log containing
sensitive data.

## Scope Boundaries

Allowed here:

- `runtime_ws` and `runtime_pull` connector behavior
- local HTTP, command, A2A, Codex, and similar adapter execution
- localhost helper behavior for delegation and progress events
- public A2A server behavior exposed by Agent Node
- CLI/runtime configuration and safety checks

Out of scope:

- Core registry storage, billing, marketplace ranking, and Cloud dashboards
- hosted payment, wallet, or withdrawal behavior
- backend-specific business logic for a particular Agent

## Runtime Rules

- Treat runtime tokens as secrets.
- Do not pass runtime tokens to backend subprocesses.
- Prefer `runtime_ws`; keep `runtime_pull` as a fallback.
- Every assigned or claimed run must receive exactly one terminal result.
- Keep adapters isolated from protocol internals where possible.
- Keep the Codex adapter in an isolated workspace.

## Pull Request Expectations

- Include tests for connector, adapter, helper, or public A2A behavior changes.
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
