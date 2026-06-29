# Contributing to OpenLinker Agent Node

Agent Node is the local runtime process for private, NAT, and local Agents. It
owns protocol connection, adapter execution, localhost helper behavior, and
terminal result delivery.

## Setup

```bash
go test ./...
go build ./cmd/openlinker-agent-node
```

## Runtime Rules

- Treat runtime tokens as secrets.
- Do not pass runtime tokens to backend subprocesses.
- Prefer `runtime_ws`; keep `runtime_pull` as a fallback.
- Every assigned or claimed run must receive exactly one terminal result.
- Keep adapters isolated from protocol internals where possible.

## Checks

```bash
gofmt -w .
go test ./...
```

