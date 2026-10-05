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
11. **支持的发布门禁：** 使用真实 tag 运行
    `node scripts/check-release-upgrade-readiness.mjs "$tag"`。唯一正式候选是
    `v0.2.0`；规范的 `v0.x.y-alpha.N`、`v0.x.y-beta.N`、`v0.x.y-rc.N` 仍为测试预发布。
    其他正式版本、v1+、格式错误和缺参/多参均拒绝，没有环境变量或工作流输入旁路。
    六平台打包必须全部通过；正式发布还须通过根仓维护的真实 Linux amd64 打包产物
    受控升级门禁，并将报告保存在 Release 中。
12. 记录精确 Core/SDK/Node 版本。同身份变更仅覆盖[受控升级指南](docs/controlled-upgrade.md)
    所列版本对和状态范围，要求 Core v0.3.0/schema 095 及管理员授权的排空、停机、
    升级/恢复操作、启动、激活流程。macOS arm64 须下载正式包单独验证，有对应二进制
    证据后才宣称通过。未覆盖的旧版本采用
    [README.zh-CN.md](./README.zh-CN.md#候选构建身份与仅限测试的重新登记) 的明确测试重新登记：
    Attempt 结算且 spool 为空后停止旧进程，不删除旧状态，使用新 NodeID、新未绑定
    有效凭据和新私有 SDK DataDir。不得静默替换版本、清空状态或自动回滚。
    active 状态下同版本普通重启与行政 drain 后恢复分别处理。
13. 宣称环境验证通过前，记录真实 WebSocket 和 pull 的登记、任务执行、正常停止/
    重启及重启后再次执行；不能只测断线重连或源码。新的测试登记不以 Core 受控升级
    扩展为前提。源码合并、发布、部署、线上验收分别记录；检查通过本身不授权发布。

## 打 tag

原生沙箱仍属实验性功能，仅供可信调用方使用。归档附带主机认证模式的中英文指南，
不再附带旧 SRT 安装器；原有归档 checksum 覆盖这些文件。打包前运行
`node --test scripts/stage-native-sandbox.test.mjs`。Node 二进制不证明主机安装的官方
客户端未被修改。当前主机认证源码的 macOS/Ubuntu CI 证据已记录在
`docs/host-auth-acceptance.md`，发布提交仍需另行验证。真实共享登录刷新与资源硬配额
未完成，Node 串行排队也不是账号级互斥。不得将预发布描述为适合不可信调用方，
也不能拿旧 SRT CI 当作新模式验收。

HOME 锁升级前须停止旧 `/tmp` 锁版本 Node，并等待或停止剩余客户端；新旧路径不互斥。
默认串行部署建议 capacity=1；多个服务须共享底层 HOME 状态存储，仅路径同名不够。

发布说明须提醒：旧的整个客户端 SRT 模式迁移至主机认证模式时，会一次性新建工作区
和客户端会话，原目录保留、不复制也不删除；后续继续复用新 scope。默认仅 Bash 的
后续加固不会再次重置已有主机认证 scope。Core 历史与 Runtime 登记/版本迁移另按各自契约处理。

明确获准发布且 CI 通过后，选择受支持且未占用的 tag。本次经审查的正式候选为：

```bash
tag=v0.2.0
node scripts/check-release-upgrade-readiness.mjs "$tag"
git tag "$tag"
git push origin "$tag"
```

Breaking change 必须在 `CHANGELOG.md` 中说明。除 v0.2.0 外的正式版本与全部 v1+
仍被拒绝。v0.2.0 正式发布不带 prerelease 标记，alpha/beta/rc 仍带该标记。
非 tag 的 workflow 仍构建精确 `sha-...` CI 产物及相邻 checksum，不发布 GitHub Release。
builder 本身还接受开发及其他精确版本身份用于测试；本地打包成功不等于获准发布。

发布前必须确认没有 Agent Token、本地 helper token、mTLS private key、invocation capability、客户输入或 adapter 日志进入仓库。
