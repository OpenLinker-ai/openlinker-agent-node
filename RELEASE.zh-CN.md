# 发布流程

English documentation: [RELEASE.md](./RELEASE.md)

OpenLinker Agent Node 从 `main` 发布，前提是 CI 和本地发布检查都通过。在 runtime 协议
和 CLI 行为足够稳定并采用严格语义化版本之前，重要变化记录在
[CHANGELOG.md](./CHANGELOG.md) 的 `Unreleased` 中。

## 发布前检查

1. 确认 `README.md`、`CONTRIBUTING.md`、`SECURITY.md`、`SUPPORT.md` 和示例是最新的。
2. 确认 `CHANGELOG.md` 描述了 runtime、adapter、helper 和 CLI 变化。
3. 运行 `gofmt -w .`。
4. 运行 `go test ./...`。
5. 运行 `go build ./cmd/openlinker-agent-node`。
6. 在干净 checkout 上运行源码 secret scan，例如 `gitleaks dir --redact .`。
7. 确认生成产物、`.env`、覆盖率输出、本地二进制、adapter 日志和私有 workspace 文件没有被跟踪。
8. 确认 runtime token 示例都使用占位值。

## 打 tag

维护者发布版本化二进制时使用语义化版本 tag：

```bash
git tag v0.x.y
git push origin v0.x.y
```

pre-1.0 版本可以包含 breaking change，但必须在 `CHANGELOG.md` 中说明。

发布前必须确认没有 runtime token、本地 helper token、客户输入或 adapter 日志进入仓库。
