# Support

Chinese documentation: [SUPPORT.zh-CN.md](./SUPPORT.zh-CN.md)

Use GitHub issues for reproducible bugs, documentation problems, and feature
requests that fit OpenLinker Agent Node's open-source scope.

## Good Issue Topics

- `runtime_ws` connection, reconnect, heartbeat, or assignment behavior
- `runtime_pull` claim/result behavior
- local HTTP, command, A2A, Codex, or helper adapter behavior
- public A2A server behavior exposed by Agent Node
- documentation gaps for local setup or runtime configuration

## Before Opening an Issue

- Search existing issues and recent commits.
- Confirm the problem on the latest `main` branch or a named release.
- Include operating system, Go version, connector mode, adapter mode, and commit
  SHA.
- Include the Core API version or commit you are testing against.
- Include reproduction steps, expected behavior, actual behavior, and sanitized
  logs.
- Redact runtime tokens, helper tokens, private URLs, customer data, command
  arguments containing secrets, and local `.env` values.

## Not Supported Here

- vulnerabilities; follow [SECURITY.md](./SECURITY.md)
- backend-specific business logic for an individual Agent
- commercial billing, wallet, withdrawal, or hosted dashboard requests
- private deployment debugging without reproducible public details

## Cross-Repository Questions

For issues that involve Core and Agent Node together, include:

- Agent Node commit SHA or binary version
- Core API commit SHA or version
- connector and adapter modes
- run ID format examples with IDs redacted if needed
- sanitized runtime logs from both sides when available
