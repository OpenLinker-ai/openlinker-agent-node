# 安全策略

English documentation: [SECURITY.md](./SECURITY.md)

不要用公开 Issue 报告安全漏洞。

优先使用 GitHub 私密漏洞报告。如果不可用，请通过 OpenLinker 公布的安全或支持渠道联系
维护者。报告中请包含受影响仓库、commit 或 release、复现步骤、影响范围，以及是否涉及
真实 token、公开 endpoint 或客户数据。

## 支持版本

OpenLinker Agent Node 目前是 pre-1.0。安全修复面向当前 `main` 分支，以及可用时的最新
release tag。除非维护者明确公告，否则旧 commit 不承诺 backport。

## 敏感区域

- Agent Token、可选外部 PKI 路径与 SDK 文件存储目录的接线，且不能暴露给 Adapter
- assignment-scoped capability 只能通过 SDK 持有的调用路径使用
- localhost helper token 作用域
- adapter 命令执行和环境变量处理
- active run 中的 A2A delegation
- public A2A server 请求处理
- Codex adapter 的 workspace 隔离

固定版本的 `openlinker-go` SDK 负责发现得到的 token-only/mTLS 安全策略、Runtime transport 与 Session 状态、
加密 journal/spool、lease fencing、resume、取消和重复执行防护。能直接在 SDK 复现的
问题请提交到 Go SDK 仓库；由本宿主接线造成的泄露或不安全行为仍在这里报告。

## 报告建议

请提供：

- 受影响 commit、tag 或二进制版本
- 脱敏后的 Runtime Session ID、Adapter 模式，以及问题能否直接在 `openlinker-go` 复现
- adapter 模式（`http`、`command`、`a2a`、`codex` 或其他）
- 最小复现和脱敏日志
- 是否暴露 Agent Token、invocation capability、mTLS key 或 helper token

不要在公开报告、测试、截图或日志里放真实第三方 secret。如果 token 已暴露，请先轮换再
分享细节。

## 披露

维护者会尽快 triage。请在修复、缓解方案或协调披露时间线确定前避免公开披露。
