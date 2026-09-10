# 发布流程

English documentation: [RELEASE.md](./RELEASE.md)

OpenLinker Agent Node 从 `main` 发布，前提是 CI 和本地发布检查都通过。Agent Node
只对 Adapter 宿主、CLI、helper 和 public A2A 接口定版本；可靠 Runtime 实现固定来自
`openlinker-go`。重要变化记录在 [CHANGELOG.md](./CHANGELOG.md) 的 `Unreleased` 中。

## 发布前检查

1. 确认 `README.md` 与 `README.zh-CN.md` 都把 Agent Node 定位为 Go SDK Runtime
   Worker 外层的独立桥接器与公开协议叶子维护者，并确认 `CONTRIBUTING`、`SECURITY`、`SUPPORT` 和示例
   是最新的。
2. 确认 `CHANGELOG.md` 描述了 Adapter、helper、CLI、public A2A 和固定 SDK 集成变化。
3. 运行 `gofmt -w .`。
4. 运行 `go test ./...`。
5. 运行 `go build ./cmd/openlinker-agent-node`。
6. 在干净 checkout 上运行源码 secret scan，例如 `gitleaks dir --redact .`。
7. 确认生成产物、`.env`、覆盖率输出、本地二进制、adapter 日志和私有 workspace 文件没有被跟踪。
8. 确认 Agent/helper token 和 mTLS 示例都使用占位值。
9. 确认 `AgentNodeVersion`、release tag、登记示例和固定 `openlinker-go` 兼容说明一致。
10. 运行 `node --test scripts/build-agent-node.test.mjs` 与真实编译的命令测试；打包统一
    使用 `scripts/build-agent-node.mjs` 注入精确 tag/commit，保留六平台与 checksum。
11. **当前候选发布阻断：** 必须先交付并验证既有 Node 的 Core 受控版本升级/回退路径，
    记录精确 Core 版本与用户操作说明，再发布本次版本注入修改。警告文案或某个私有
    迁移选择新 NodeID 都不能替代产品兼容门禁。正常停止/重启须覆盖 WebSocket 和 pull，
    不能只测断线重连，也不能提供尚不存在的命令。
    tag 打包链当前执行 `scripts/check-release-upgrade-readiness.mjs` 并明确失败；没有
    环境变量/工作流参数旁路，只有完成受审 Core 兼容集成后才可替换该阻断。

## 打 tag

维护者发布版本化二进制时使用语义化版本 tag：

```bash
git tag v0.x.y
git push origin v0.x.y
```

pre-1.0 版本可以包含 breaking change，但必须在 `CHANGELOG.md` 中说明。

发布前必须确认没有 Agent Token、本地 helper token、mTLS private key、invocation capability、客户输入或 adapter 日志进入仓库。
