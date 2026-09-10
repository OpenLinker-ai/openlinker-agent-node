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
11. **仅限测试的预发布门禁：** 使用真实 tag 运行
    `node scripts/check-release-upgrade-readiness.mjs "$tag"`。仅规范的
    `v0.x.y-alpha.N`、`v0.x.y-beta.N`、`v0.x.y-rc.N` 通过，数字为无前导零的非负整数。
    缺参/多参、稳定版、v1+ 和格式错误均拒绝，没有工作流参数或环境变量旁路。
    workflow 在 tag 打包前传入真实 `GITHUB_REF_NAME`，全部六平台任务通过后才创建或
    更新 GitHub Release，并强制标记为 **prerelease**。
12. 记录选用的 Core/SDK/Node 版本及
    [README.zh-CN.md](./README.zh-CN.md#候选构建身份与仅限测试的重新登记) 中的重新登记流程。
    此策略仅适用于无真实用户的测试部署：旧 Attempt 结算且 spool 为空后停止旧进程，
    不删除旧状态，再使用新 NodeID、新未绑定有效凭据与新私有 SDK DataDir。
    不支持旧 Node 直接换版本或自动回滚。active Node 可用相同精确版本、同 DataDir
    普通重启，这不是行政 drain/activate 恢复。不得宣称具备通用迁移控制器、原地升级
    或本机操作系统级隔离。
13. 宣称环境验证通过前，记录真实 WebSocket 和 pull 的登记、任务执行、正常停止/
    重启及重启后再次执行；不能只测断线重连或源码。新的测试登记不以 Core 受控升级
    扩展为前提。源码合并、发布、部署、线上验收分别记录；检查通过本身不授权发布。

## 打 tag

明确获准发布后，选择未占用的规范 pre-1.0 测试预发布 tag（下列仅为示例，非预定版本）：

```bash
tag=v0.1.59-rc.1
node scripts/check-release-upgrade-readiness.mjs "$tag"
git tag "$tag"
git push origin "$tag"
```

Breaking change 必须在 `CHANGELOG.md` 中说明。稳定 pre-1.0 和 v1+ 发布仍被拒绝。
非 tag 的 workflow 仍构建精确 `sha-...` CI 产物及相邻 checksum，不发布 GitHub Release。
builder 本身还接受开发及其他精确版本身份用于测试；本地打包成功不等于获准发布。

发布前必须确认没有 Agent Token、本地 helper token、mTLS private key、invocation capability、客户输入或 adapter 日志进入仓库。
