# 会话隔离源码验收 — 2026-09-12

## 源码范围

- 修改仓库：`OpenLinker-ai/openlinker-agent-node`。
- 工作区：`openlinker-node-session-isolation`。
- 分支：`codex/session-process-isolation`。
- 本次 PR 基础：共享 Codex 轮次提交 `8e975bef7c6ff9af490deb52da49a7070fcbdefc`（Node PR #30）。
- P1 已独立合并为 `e272a32905fa1afc56fc265ba50a2377715f2068` 并发布 `v0.1.57-rc.3`；本 PR 不重复携带 P1。
- 状态：独立待审功能候选，源码与 CI 状态以对应 PR 为准；未合并、发布或部署 Docker 隔离。
- 本 PR 仅修改 Node；Plugin 的 P2 共享接线在独立 PR。SDK、CLI、运行中的 Agent 配置及根仓子模块 pin 未修改。

新增 Docker 会话进程隔离属于 Node 的普通客户端桥接。SDK 继续独占 Runtime Worker
和可靠传输，未新增 Worker、Browser 服务或 Plugin 依赖。默认原生执行方式不变。
Codex 沙箱仅通过 4 行命令工厂接线进入 `codexturn`，不再复制或修改通用轮次流程。
通用入口与 Start 前的取消检查都由共享叶子提供，Plugin 同样复用该实现。

## 已执行验证

环境为 macOS arm64，Go 1.27.1，本机 Docker Desktop 的 Linux arm64 daemon。

| 检查 | 结果 |
| --- | --- |
| `GOWORK=off go test -race ./...` | 全包通过；真实 Docker 用例在普通测试中按约定跳过，另行运行 |
| 新增配置、权限范围、目录别名与取消前置检查的定向 race 测试 | 通过 |
| `sh scripts/test-session-isolation.sh` | 真实容器验收通过，两种协议分别验证续接和取消 |
| 根 workspace 组合 | 前轮验证已通过；本次拆分后另验证 Node/Plugin 组合，具体最终结果记录于根仓拆分交付报告 |
| `node scripts/check-adapter-boundaries.mjs` | 通过；42 个模块，六个平台的 24 组测试包图、6 组生产包图 |
| `node scripts/generate-codex-rpc.mjs --check` | 通过 |
| `GOWORK=off go vet ./...`、`go build ./cmd/openlinker-agent-node` | 通过 |
| `GOWORK=off go mod verify`、`git diff --check` | 通过 |

Docker 验收使用本次编译的 Linux Go 协议夹具和 scratch 镜像，不调用模型、不读取
真实账号凭据。它实际在容器内读写文件、启动子进程并执行 Codex RPC / Claude JSONL：

1. 每一轮从独立的宿主测试进程调用生产 `NewProvider` / `Provider.Run`。
2. A 保存原生 ID 和工作文件；B 拿不到 A 的文件，也不能通过输入中的 ID 恢复 A。
3. 新 Node 进程再调用 A，原生 ID 不变，工作文件保留，结果明确报告已 resume。
4. 相同会话字符串在不同调用者、Agent、Core 命名空间和客户端类型下不共享状态。
5. 容器读取宿主 canary、其他会话文件、Node 私有映射、符号链接跳转均被阻断。
6. 非 root、只读镜像、无 Docker socket、无宿主 Agent Token、默认无非 loopback 网卡；
   容器内普通子进程可运行。
7. 取消两种客户端运行后，持续写 heartbeat 的子进程停止。
8. 跨宿主进程的同会话锁互斥；模拟 Node 死亡后的锁释放，下一次 Open 清理残留容器。
9. 已持久化的恶意 HOME 符号链接被拒绝，缺失镜像不回退宿主执行；同名但所有权标记
   不匹配的容器被保留并导致明确失败。
10. Docker daemon 身份不匹配时拒绝复用，防止切换 daemon 绕过旧容器的清理。

前轮初始候选还在本机已有的真实客户端镜像里验证过版本和帮助能力（本次拆分回归未重跑，不能替代本次模型执行验收）：

| 客户端 | 镜像 ID | 结果 |
| --- | --- | --- |
| Codex 0.153.0 | `sha256:89466f7a51b33495055ee5e97781d77ac49197845324e1ef150820271fdbc607` | 通过 |
| Claude 2.1.259 | `sha256:412e1f73b0558f4fe73d273f8ace1041847a85282292838211bf5f3bd3a2191c` | 通过 |

这两项仅运行 `--version` / 帮助参数，不构成真实模型或工具链路已通过的证明，也不代表
上述历史镜像采用了当前 Plugin main。测试产生的容器和 scratch 验收镜像已清理。

## 交付限制

CI 配置保留真实 Docker 验收步骤。PR 中的 CI 结论以当前 head 的 GitHub 检查为准；
本记录中的本地容器回归不代表隔离安全模型已经获批。
完整启用方式见 [中文配置说明](session-isolation.zh-CN.md)。隔离模式默认关闭；需要
专用 API 凭据和本机镜像。默认断网，联网运行必须显式配置出口网络。个人 OAuth/钥匙串、
宿主 MCP 委托、Windows Node、远程 Docker 和真实模型工具调用未在本轮实现/验收。
现有运行中 Agent 没有切换到新模式。

本地详细日志位于 `/tmp/openlinker-isolation-after-split-*.log`（初测为 `/tmp/openlinker-session-*.log`）；这些日志用于本次会话复查，不属于发布产物。
