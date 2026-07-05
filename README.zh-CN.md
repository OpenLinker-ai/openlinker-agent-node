# OpenLinker Agent Node

OpenLinker Agent Node 是面向自托管、本地、私有网络和 NAT 后 AI Agent 的开源 runtime
连接器。它通过 `runtime_ws` 或 `runtime_pull` 把 Agent 接入 OpenLinker AI Agent
注册中心和运行时网关，调用本地 HTTP / command / A2A / Codex adapter，回传 run 事件
和结果，并支持 Agent-to-Agent delegation，同时不把真实 runtime token 暴露给后端子进程。

English documentation: [README.md](./README.md)

如果你的 Agent 已经有稳定 HTTPS endpoint 或远程 MCP endpoint，Core 通常可以直接调用，
不一定需要 Agent Node。

## 状态

Agent Node 目前是 pre-1.0。Core runtime 协议稳定前，runtime 消息结构、adapter 选项和
CLI 行为仍可能变化。

## 连接策略

优先使用最简单可用的连接模式：

1. `direct_http`：OpenLinker Core 可访问稳定 HTTPS 调用 endpoint。
2. `mcp_server`：Agent 已暴露远程 HTTP JSON-RPC / MCP tools endpoint。
3. `runtime_ws`：适合本地、内网或 NAT 后 Agent。Agent Node 主动建立 WebSocket 并实时接收 run。
4. `runtime_pull`：只有 WebSocket 无法稳定连接或环境阻断时才作为 fallback。

## 快速开始

依赖：

- Go 1.25 或更高版本
- 已在 OpenLinker Core 注册的 Agent
- Agent runtime token
- 本地后端进程、命令、Codex workspace 或上游 A2A endpoint

构建和测试：

```bash
go test ./...
go build ./cmd/openlinker-agent-node
```

对接本地 HTTP 后端：

```bash
OPENLINKER_API_BASE=https://api.openlinker.ai \
OPENLINKER_AGENT_TOKEN=ol_agent_xxx \
OPENLINKER_AGENT_NODE_CONNECTOR=runtime_ws \
OPENLINKER_AGENT_NODE_ADAPTER=openclaw \
OPENLINKER_AGENT_NODE_HTTP_URL=http://127.0.0.1:18080/run \
go run ./cmd/openlinker-agent-node
```

## 后端请求 Envelope

本地 HTTP 后端会收到 JSON envelope：

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

helper endpoint 是本地且按 run 作用域限制的。后端应使用 helper 做 delegation 和进度事件，
不要接收真实 Agent runtime token。

## Adapter 模式

### `http` / `openclaw`

把 run envelope POST 到本地 HTTP 后端。

```bash
OPENLINKER_AGENT_NODE_ADAPTER=openclaw
OPENLINKER_AGENT_NODE_HTTP_URL=http://127.0.0.1:18080/run
```

### `command`

运行本地命令，并把任务 envelope 写入 stdin。

```bash
OPENLINKER_AGENT_NODE_ADAPTER=command
OPENLINKER_AGENT_NODE_COMMAND=/usr/local/bin/my-agent
OPENLINKER_AGENT_NODE_ARGS='["run","--json"]'
```

### `a2a`

把 OpenLinker 分配的 run 转发给本地或远程 A2A JSON-RPC Agent。

```bash
OPENLINKER_AGENT_NODE_ADAPTER=a2a
OPENLINKER_AGENT_NODE_A2A_BASE_URL=http://127.0.0.1:31225/rpc
OPENLINKER_AGENT_NODE_A2A_METHOD=SendMessage
```

只有当上游 Agent 仍使用旧 slash-style 方法（如 `message/send`）时，才设置
`OPENLINKER_AGENT_NODE_A2A_DIALECT=legacy`。

### `codex`

非交互运行 Codex。请把该 adapter 放在隔离 workspace 中。

```bash
OPENLINKER_AGENT_NODE_ADAPTER=codex
OPENLINKER_AGENT_NODE_CODEX_BIN=codex
OPENLINKER_AGENT_NODE_CODEX_WORKSPACE=/srv/openlinker/codex-work
OPENLINKER_AGENT_NODE_CODEX_SANDBOX=workspace-write
```

## Runtime 模式

WebSocket 是默认且推荐模式：

```bash
OPENLINKER_AGENT_NODE_CONNECTOR=runtime_ws
```

受限网络可强制使用 pull fallback：

```bash
OPENLINKER_AGENT_NODE_CONNECTOR=runtime_pull
```

两种模式使用同一套 Core run 生命周期。每个 assigned / claimed run 必须提交一次终态结果。

## A2A Delegation

后端可以在处理 run 时调用另一个 Agent。Agent Node 提供当前 run 上下文，并把真实 token
保留在节点内部。

`http`、`command` 和 `codex` adapter 默认启用 localhost helper。command 后端还会收到：

```bash
OPENLINKER_AGENT_NODE_HELPER_URL
OPENLINKER_AGENT_NODE_HELPER_TOKEN
OPENLINKER_AGENT_NODE_HELPER_CALL_AGENT_URL
OPENLINKER_AGENT_NODE_HELPER_EVENTS_URL
```

## Public A2A Server

Agent Node 可选把本地后端暴露成一个小型 public A2A server。只有本地进程确实需要接收入站
A2A 流量时才开启：

```bash
OPENLINKER_AGENT_NODE_PUBLIC_A2A=true
OPENLINKER_AGENT_NODE_PUBLIC_A2A_HOST=127.0.0.1
OPENLINKER_AGENT_NODE_PUBLIC_A2A_PORT=19091
OPENLINKER_AGENT_NODE_PUBLIC_A2A_SLUG=my-agent
OPENLINKER_AGENT_NODE_PUBLIC_A2A_NAME="My Agent"
OPENLINKER_PUBLIC_A2A_TOKEN=optional-bearer-token
```

## 开发

```bash
gofmt -w .
go test ./...
go build ./cmd/openlinker-agent-node
```

## 安全

- runtime token 必须视为 secret。
- 不要把 runtime token 传给后端子进程。
- `codex` adapter 使用隔离 workspace。
- 公开 Issue 前删除 runtime token、helper token、私有 URL 和本地日志。

漏洞请通过 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md) 报告。

## 贡献

提交 PR 前请阅读 [CONTRIBUTING.zh-CN.md](./CONTRIBUTING.zh-CN.md)。协议连接、adapter
执行、本地 helper 和最终结果提交是本仓库核心边界，不要让后端子进程直接接触真实
runtime token。

## 支持和发布

- 支持说明：[SUPPORT.zh-CN.md](./SUPPORT.zh-CN.md)
- 发布清单：[RELEASE.zh-CN.md](./RELEASE.zh-CN.md)
- 英文变更记录：[CHANGELOG.md](./CHANGELOG.md)
- 行为准则：[CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)

## 许可证

Apache-2.0。详见 [LICENSE](./LICENSE)。
