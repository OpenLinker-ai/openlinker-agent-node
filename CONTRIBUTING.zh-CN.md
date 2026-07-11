# 贡献 OpenLinker Agent Node

English documentation: [CONTRIBUTING.md](./CONTRIBUTING.md)

感谢你改进 OpenLinker Agent Node。本仓库负责本地 runtime 进程，让私有、本地或 NAT
环境中的 Agent 连接到 OpenLinker Core。

## 开发环境

```bash
go test ./...
go build ./cmd/openlinker-agent-node
```

测试和文档只能使用占位 Agent/helper token。不要提交真实 token、mTLS private key、
invocation capability、私有 endpoint、本地 `.env`、客户 payload 或包含敏感数据的 adapter 日志。

## 范围边界

可以放在这里：

- Runtime v2 HTTP long-poll、assignment confirmation、lease、command 和 resume 行为
- assignment、Event 和 Result 的持久化 journal/spool 行为
- 本地 HTTP、command、A2A、Codex 等 adapter 执行
- delegation 和进度事件的 localhost helper 行为
- Agent Node 暴露的 public A2A server 行为
- CLI/runtime 配置和安全检查

不要放在这里：

- Core registry 存储、计费、市场排序和 Cloud Dashboard
- 托管支付、钱包或提现行为
- 某个具体 Agent 的后端业务逻辑

## Runtime 规则

- Agent Token、mTLS key、invocation capability、spool key 和 helper token 都是 secret。
- 不要把 Agent Token 或 invocation capability 传给后端子进程。
- assignment 必须先持久化再 ACK，只有 Core confirmation 后才能执行。
- Event/Result 在收到身份匹配的 typed ACK 前必须沿用稳定 ID。
- 进程崩溃后，不得在缺少可持久化进程 checkpoint 的情况下重跑已经 started 的 Attempt。
- Agent 子调用必须显式提供幂等 key：同一意图重试复用，新意图换 key。
- adapter 尽量与协议内部实现隔离。
- Codex adapter 必须使用隔离 workspace。

## PR 要求

- runtime transport、durability、adapter、helper 或 public A2A 行为变化需要测试。
- 新增环境变量要写入 `README.md`。
- 说明对已有 runtime 的兼容性影响。
- 删除日志或 fixture 中的 token 和本地路径。

## 检查

```bash
gofmt -w .
go test ./...
go build ./cmd/openlinker-agent-node
```

## 安全

不要公开提交漏洞 Issue。请按照 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md) 处理。

## 许可证

贡献即表示你同意贡献内容使用本仓库的 Apache-2.0 许可证。
