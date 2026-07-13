# Support

Chinese documentation: [SUPPORT.zh-CN.md](./SUPPORT.zh-CN.md)

Use GitHub issues for reproducible bugs, documentation problems, and feature
requests that fit OpenLinker Agent Node's open-source scope.

## Good Issue Topics

- local HTTP, command, A2A, Codex, or helper adapter behavior
- CLI and environment parsing, process-tree control, or SDK configuration wiring
- public A2A server behavior exposed by Agent Node
- documentation gaps for local setup or Adapter configuration
- Runtime failures that reproduce only through the Agent Node host integration

## Before Opening an Issue

- Search existing issues and recent commits.
- Confirm the problem on the latest `main` branch or a named release.
- Include operating system, Go version, adapter mode, and Agent Node commit SHA.
- Include the Core API version or commit you are testing against.
- Include reproduction steps, expected behavior, actual behavior, and sanitized
  logs.
- Redact Agent/helper tokens, mTLS material, invocation capabilities, private
  URLs, customer data, secret-bearing command arguments, and local `.env` values.

## Not Supported Here

- vulnerabilities; follow [SECURITY.md](./SECURITY.md)
- Runtime discovery, mTLS, WebSocket/Pull, Session, claim, lease, resume,
  journal/spool, ACK repair, cancellation, or drain defects that reproduce in
  `openlinker-go`; report those in the Go SDK repository
- backend-specific business logic for an individual Agent
- commercial billing, wallet, withdrawal, or hosted dashboard requests
- private deployment debugging without reproducible public details

## Cross-Repository Questions

For issues that involve Core and Agent Node together, include:

- Agent Node commit SHA or binary version
- pinned `openlinker-go` version
- Core API commit SHA or version
- Adapter mode and whether the failure is in the host integration or SDK Worker
- run ID format examples with IDs redacted if needed
- sanitized Agent Node and Core logs when available
