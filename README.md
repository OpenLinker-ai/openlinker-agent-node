# OpenLinker Agent Node

Agent Node is the local protocol process for OpenLinker Agents. It keeps an
Agent online through `runtime_ws`, invokes a backend adapter, reports events and
results, and lets the backend call other Agents through A2A.

## Connection Policy

Use Agent Node only when OpenLinker cannot directly invoke the Agent backend.
The preferred order is:

1. `direct_http`: OpenLinker can reach a stable HTTPS invocation endpoint.
2. `mcp_server`: the Agent already exposes a remote HTTP JSON-RPC / MCP tools
   endpoint.
3. `runtime_ws`: local, private-network, or NAT Agents. Agent Node opens an
   outbound WebSocket and receives real-time run assignments.
4. `runtime_pull`: fallback only when WebSocket cannot stay connected or is
   blocked by the environment.

## Quick Start

```bash
cd openlinker-agent-node
go test ./...
go build ./cmd/openlinker-agent-node

OPENLINKER_API_BASE=https://api.openlinker.ai \
OPENLINKER_RUNTIME_TOKEN=ol_live_xxx \
OPENLINKER_AGENT_NODE_ADAPTER=openclaw \
OPENLINKER_AGENT_NODE_HTTP_URL=http://127.0.0.1:18080/run \
go run ./cmd/openlinker-agent-node
```

The local HTTP backend receives an envelope:

```json
{
  "input": { "query": "..." },
  "run_id": "run uuid",
  "metadata": {},
  "a2a": {},
  "agent_node": {
    "helper": {
      "endpoints": {
        "call_agent": "http://127.0.0.1:12345/a2a/call",
        "events": "http://127.0.0.1:12345/events"
      }
    }
  }
}
```

## Adapter Modes

### http / openclaw

POSTs to a local backend, useful for OpenClaw/Xiaolongxia-style local services.

```bash
OPENLINKER_AGENT_NODE_ADAPTER=openclaw
OPENLINKER_AGENT_NODE_HTTP_URL=http://127.0.0.1:18080/run
```

### command

Runs a local command and writes the task envelope to stdin.

```bash
OPENLINKER_AGENT_NODE_ADAPTER=command
OPENLINKER_AGENT_NODE_COMMAND=/usr/local/bin/xiaolongxia
OPENLINKER_AGENT_NODE_ARGS='["run","--json"]'
```

### a2a

Forwards assigned OpenLinker runs to a local or remote A2A JSON-RPC Agent. Use
this when the backend already speaks A2A and Agent Node only needs to keep the
runtime connection open for NAT or private-network deployment.

```bash
OPENLINKER_API_BASE=https://api.openlinker.ai \
OPENLINKER_RUNTIME_TOKEN=ol_live_xxx \
OPENLINKER_AGENT_NODE_CONNECTOR=runtime_ws \
OPENLINKER_AGENT_NODE_ADAPTER=a2a \
OPENLINKER_AGENT_NODE_A2A_BASE_URL=http://127.0.0.1:31225/rpc \
OPENLINKER_AGENT_NODE_A2A_METHOD=SendMessage \
go run ./cmd/openlinker-agent-node
```

By default, Agent Node builds a blocking A2A 1.0 `SendMessage` request from
`input.text`, `input.query`, `input.task`, or `input.prompt`. To pass raw A2A
params, set `input.a2a_params` or `input.params`. Set
`OPENLINKER_AGENT_NODE_A2A_DIALECT=legacy` only when the upstream Agent still
expects the older slash-style methods such as `message/send` and legacy `kind`
fields on message parts.

### codex

Runs Codex non-interactively. Keep this adapter in an isolated workspace.

```bash
OPENLINKER_AGENT_NODE_ADAPTER=codex
OPENLINKER_AGENT_NODE_CODEX_BIN=codex
OPENLINKER_AGENT_NODE_CODEX_WORKSPACE=/srv/openlinker/codex-work
OPENLINKER_AGENT_NODE_CODEX_SANDBOX=workspace-write
```

## Runtime Modes

Default mode is WebSocket. This is the preferred Agent Node mode for NAT or
private-network Agents:

```bash
OPENLINKER_AGENT_NODE_CONNECTOR=runtime_ws
```

Pull fallback can be forced for tests or degraded networks, but it should not be
the first choice when WebSocket works:

```bash
OPENLINKER_AGENT_NODE_CONNECTOR=runtime_pull
```

## A2A Delegation

Backends can call another Agent while processing a run:

The node supplies `current_run_id` from the assigned run context and uses the
runtime token. Backends do not manage parent run IDs.

For `http`, `command`, and `codex` adapters, Agent Node enables a localhost
helper by default. The backend receives `agent_node.helper` in its JSON envelope
and command backends also receive:

```bash
OPENLINKER_AGENT_NODE_HELPER_URL
OPENLINKER_AGENT_NODE_HELPER_TOKEN
OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL
OPENLINKER_AGENT_NODE_HELPER_EVENTS_URL
```

Call another Agent:

```bash
curl -X POST "$OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL" \
  -H "Authorization: Bearer $OPENLINKER_AGENT_NODE_HELPER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"target_agent_id":"target-agent-uuid","reason":"delegate","input":{"query":"hello"}}'
```

Emit progress:

```bash
curl -X POST "$OPENLINKER_AGENT_NODE_HELPER_EVENTS_URL" \
  -H "Authorization: Bearer $OPENLINKER_AGENT_NODE_HELPER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"event_type":"run.message.delta","payload":{"text":"working"}}'
```

The helper token is local and run-scoped. The real runtime token stays inside
Agent Node.

Helper settings:

```bash
OPENLINKER_AGENT_NODE_HELPER=auto   # auto | true | false
OPENLINKER_AGENT_NODE_HELPER_HOST=127.0.0.1
OPENLINKER_AGENT_NODE_HELPER_PORT=0
```

## Public A2A Server

Agent Node can optionally expose the local backend as a small public A2A server.
Keep this off unless the local process is meant to accept inbound A2A traffic:

```bash
OPENLINKER_AGENT_NODE_PUBLIC_A2A=true
OPENLINKER_AGENT_NODE_PUBLIC_A2A_HOST=127.0.0.1
OPENLINKER_AGENT_NODE_PUBLIC_A2A_PORT=19091
OPENLINKER_AGENT_NODE_PUBLIC_A2A_SLUG=my-agent
OPENLINKER_AGENT_NODE_PUBLIC_A2A_NAME="My Agent"
OPENLINKER_AGENT_NODE_PUBLIC_A2A_TOKEN=optional-bearer-token
```

The public server supports Agent Card, extended card, JSON-RPC, HTTP+JSON
send/stream, task get/list/subscribe/cancel, and task Push Notification Config
CRUD. Push Config is memory-backed inside the Agent Node process; use Core's
platform A2A adapter for durable webhook/callback subscriptions. Agent Node
does not advertise gRPC. Core gRPC is a separate external A2A binding exposed
by Core when `A2A_GRPC_ENABLED=true`.

## Contributing and Security

See [CONTRIBUTING.md](./CONTRIBUTING.md) for development rules and
[CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md) for conduct expectations.
Use [SUPPORT.md](./SUPPORT.md) for help, [SECURITY.md](./SECURITY.md) for
vulnerability reporting, [CHANGELOG.md](./CHANGELOG.md) for release notes, and
[RELEASE.md](./RELEASE.md) for release checks.

## License

Apache-2.0. See [LICENSE](./LICENSE).
