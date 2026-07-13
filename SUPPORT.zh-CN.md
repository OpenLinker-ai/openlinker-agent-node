# 支持

English documentation: [SUPPORT.md](./SUPPORT.md)

可用 GitHub Issues 报告可复现 bug、文档问题，以及符合 OpenLinker Agent Node 开源范围
的功能请求。

## 适合提交 Issue 的内容

- Runtime WebSocket 或 HTTPS 长轮询的 session、claim、heartbeat 与 command 行为
- assignment ACK/confirmation、lease renewal、durable Event/Result 或恢复行为
- 本地 HTTP、command、A2A、Codex 或 helper adapter 行为
- Agent Node 暴露的 public A2A server 行为
- 本地设置或 runtime 配置文档缺口

## 提交前请确认

- 搜索已有 Issue 和近期 commit。
- 在最新 `main` 或指定 release 上确认问题。
- 提供操作系统、Go 版本、adapter 模式和 Agent Node commit SHA。
- 提供正在测试的 Core API 版本或 commit。
- 提供复现步骤、期望行为、实际行为和脱敏日志。
- 删除 Agent/helper token、mTLS 材料、invocation capability、私有 URL、客户数据、含 secret 的命令参数和本地 `.env`。

## 不在这里处理

- 安全漏洞；请看 [SECURITY.zh-CN.md](./SECURITY.zh-CN.md)
- 某个具体 Agent 的后端业务逻辑
- 商业计费、钱包、提现或托管 Dashboard 请求
- 无法公开复现的私有部署调试

## 跨仓库问题

涉及 Core 和 Agent Node 的问题请包含：

- Agent Node commit SHA 或二进制版本
- Core API commit SHA 或版本
- adapter 模式，以及故障发生在正常执行还是恢复阶段
- 可脱敏的 run ID 示例
- 可用时提供两侧脱敏 runtime 日志
