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
npm install

OPENLINKER_API_BASE=https://api.openlinker.ai \
OPENLINKER_RUNTIME_TOKEN=ol_live_xxx \
OPENLINKER_AGENT_NODE_ADAPTER=module \
OPENLINKER_AGENT_NODE_MODULE=./my-agent.mjs \
npm start
```

`my-agent.mjs`:

```js
export async function handle(input, ctx) {
  ctx.emit("run.message.delta", { text: "working" });
  return { ok: true, input };
}
```

## Adapter Modes

### module

Loads a local ESM module.

```bash
OPENLINKER_AGENT_NODE_ADAPTER=module
OPENLINKER_AGENT_NODE_MODULE=./agent.mjs
```

### http

POSTs to a local backend, useful for OpenClaw/Xiaolongxia-style local services.

```bash
OPENLINKER_AGENT_NODE_ADAPTER=http
OPENLINKER_AGENT_NODE_HTTP_URL=http://127.0.0.1:18080/run
```

### command

Runs a local command and writes the task envelope to stdin.

```bash
OPENLINKER_AGENT_NODE_ADAPTER=command
OPENLINKER_AGENT_NODE_COMMAND=xiaolongxia
OPENLINKER_AGENT_NODE_ARGS='["run","--json"]'
```

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

```js
export async function handle(input, ctx) {
  const child = await ctx.callAgent("target-agent-uuid", {
    query: input.query,
  }, {
    reason: "delegate search",
  });

  return { child };
}
```

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
