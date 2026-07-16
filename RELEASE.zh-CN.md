# 发布流程

English documentation: [RELEASE.md](./RELEASE.md)

OpenLinker Agent Node 从 `main` 发布，前提是 CI 和本地发布检查都通过。Agent Node
只对 Adapter 宿主、CLI、helper 和 public A2A 接口定版本；可靠 Runtime 实现固定来自
`openlinker-go`。重要变化记录在 [CHANGELOG.md](./CHANGELOG.md) 的 `Unreleased` 中。

## 发布前检查

1. 确认 `README.md` 与 `README.zh-CN.md` 都把 Agent Node 定位为 Go SDK Runtime
   Worker 外层的临时 Adapter，并确认 `CONTRIBUTING`、`SECURITY`、`SUPPORT` 和示例
   是最新的。
2. 确认 `CHANGELOG.md` 描述了 Adapter、helper、CLI、public A2A 和固定 SDK 集成变化。
3. 运行 `gofmt -w .`。
4. 运行 `go test ./...`。
5. 运行 `go build ./cmd/openlinker-agent-node`。
6. 在干净 checkout 上运行源码 secret scan，例如 `gitleaks dir --redact .`。
7. 确认生成产物、`.env`、覆盖率输出、本地二进制、adapter 日志和私有 workspace 文件没有被跟踪。
8. 确认 Agent/helper token 和 mTLS 示例都使用占位值。
9. 确认 `AgentNodeVersion`、release tag、登记示例和固定 `openlinker-go` 兼容说明一致。

## 打 tag

维护者发布版本化二进制时使用语义化版本 tag：

```bash
git tag v0.x.y
git push origin v0.x.y
```

pre-1.0 版本可以包含 breaking change，但必须在 `CHANGELOG.md` 中说明。

发布前必须确认没有 Agent Token、本地 helper token、mTLS private key、invocation capability、客户输入或 adapter 日志进入仓库。
