# 贡献 OpenLinker Agent Node

English documentation: [CONTRIBUTING.md](./CONTRIBUTING.md)

感谢你改进 OpenLinker Agent Node。本仓库为 HTTP、command、A2A、Codex backend 提供
轻量 Adapter 进程；可靠 Runtime Worker 由固定版本的 `openlinker-go` SDK 负责。

## 开发环境

```bash
go test ./...
go build ./cmd/openlinker-agent-node
```

测试和文档只能使用占位 Agent/helper token。不要提交真实 token、mTLS private key、
invocation capability、私有 endpoint、本地 `.env`、客户 payload 或包含敏感数据的 adapter 日志。

## 范围边界

可以放在这里：

- 本地 HTTP、command、A2A、Codex 等 adapter 执行
- delegation 和进度事件的 localhost helper 行为
- Agent Node 暴露的 public A2A server 行为
- CLI 和环境变量解析、SDK 配置、进程控制与文件存储目录选择
- 可复现到本宿主进程与固定 SDK 边界的集成问题

不要放在这里：

- Core registry 存储、计费、市场排序和 Cloud Dashboard
- 托管支付、钱包或提现行为
- 某个具体 Agent 的后端业务逻辑
- Runtime 发现、mTLS、transport 切换、Session 状态、journal/spool、lease、resume、
  取消、drain 与 ACK 修复；这些属于 `openlinker-go`

## Runtime 规则

- Agent Token、mTLS key、invocation capability、SDK store key 和 helper token 都是 secret。
- 不要把 Agent Token 或 invocation capability 传给后端子进程。
- Agent 子调用必须显式提供幂等 key：同一意图重试复用，新意图换 key。
- adapter 必须与 Runtime 协议内部实现隔离；不要在这里增加第二套 transport、journal、
  spool 或交付状态机。
- Codex adapter 必须使用隔离 workspace。

## PR 要求

- Adapter、CLI、helper、进程控制、SDK 集成或 public A2A 行为变化需要测试。
- 新增环境变量要写入 `README.md`。
- 说明对已有 Adapter 部署的兼容性影响。
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
