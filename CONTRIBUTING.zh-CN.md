# 贡献 OpenLinker Agent Node

English documentation: [CONTRIBUTING.md](./CONTRIBUTING.md)

感谢你改进 OpenLinker Agent Node。本仓库负责本地 runtime 进程，让私有、本地或 NAT
环境中的 Agent 连接到 OpenLinker Core。

## 开发环境

```bash
go test ./...
go build ./cmd/openlinker-agent-node
```

测试和文档只能使用占位 runtime token。不要提交真实 token、私有 endpoint、本地 `.env`、
客户 payload 或包含敏感数据的 adapter 日志。

## 范围边界

可以放在这里：

- `runtime_ws` 和 `runtime_pull` connector 行为
- 本地 HTTP、command、A2A、Codex 等 adapter 执行
- delegation 和进度事件的 localhost helper 行为
- Agent Node 暴露的 public A2A server 行为
- CLI/runtime 配置和安全检查

不要放在这里：

- Core registry 存储、计费、市场排序和 Cloud Dashboard
- 托管支付、钱包或提现行为
- 某个具体 Agent 的后端业务逻辑

## Runtime 规则

- runtime token 是 secret。
- 不要把 runtime token 传给后端子进程。
- 优先使用 `runtime_ws`，`runtime_pull` 作为 fallback。
- 每个 assigned / claimed run 必须收到一次终态结果。
- adapter 尽量与协议内部实现隔离。
- Codex adapter 必须使用隔离 workspace。

## PR 要求

- connector、adapter、helper 或 public A2A 行为变化需要测试。
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
