# Support

Chinese documentation: [SUPPORT.zh-CN.md](./SUPPORT.zh-CN.md)

Use GitHub issues for reproducible bugs, documentation problems, and feature
requests that fit OpenLinker Agent Node's open-source scope.

## Good Issue Topics

- Runtime WebSocket or HTTPS long-poll session, claim, heartbeat, or command behavior
- assignment ACK/confirmation, lease renewal, durable Event/Result, or recovery behavior
- local HTTP, command, A2A, Codex, or helper adapter behavior
- public A2A server behavior exposed by Agent Node
- documentation gaps for local setup or runtime configuration

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
- backend-specific business logic for an individual Agent
- commercial billing, wallet, withdrawal, or hosted dashboard requests
- private deployment debugging without reproducible public details

## Cross-Repository Questions

For issues that involve Core and Agent Node together, include:

- Agent Node commit SHA or binary version
- Core API commit SHA or version
- adapter mode and whether the failure happened during normal execution or recovery
- run ID format examples with IDs redacted if needed
- sanitized runtime logs from both sides when available
