# Codex 任务输入图片链接（设计）

> 状态：2026-11-12 设计经用户确认并在本分支实现；源码合并、二进制发布、部署与实机验收分别记录，本文不代表已发布或已部署。

## 背景与归属

买家在服务下单表单中只能提交 text/url 字段。原生 Codex 适配器目前只把整个 `input`
JSON 作为文本 prompt 发送，模型看不到图片像素；原生隔离的工具网络只放行精确域名白名单，
`web_search` 只返回网页文本，因此 Agent 无法自行读取任意图片链接。

本能力是 **Agent Node 的桥接职责**：Node 在沙箱外按固定安全规则下载图片，再以
Codex app-server 已有的 `localImage` 输入随本轮文本一起发送。不改 SDK、CLI 或 Plugin，
不新增依赖方向；Core 仍是授权、调度与终态权威。

## 开关（默认关闭）

- 环境变量 `OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT`，取值与现有布尔开关一致
  （`true/1/yes/on`、`false/0/no/off`；未设置为关闭；其他值启动时报错，不回显原值）。
- 关闭时完全不识别字段：prompt 文本与 `turn/start` 输入与当前版本逐字节相同。
- 新增 `ProviderConfig.CodexImageInput bool`，只由 Codex 适配器读取。

## 识别规则（显式约定，不猜测任意 URL）

- 只看 `input` 为 JSON 对象时的**顶层**字段；字段名为 `image_url` 或以 `_image_url` 结尾，
  且值为字符串（例如 `image_url`、`reference_image_url`）。
- 嵌套对象、数组、其他字段中的 URL 一律不下载，仍按原样出现在文本 prompt 中。
- 每个 Run 最多 4 张；超过直接失败，不静默截断。空字符串字段视为未提供。
- URL 长度上限 2048，必须是绝对 `https://` URL，不含用户名/密码，端口只能是 443（或省略），
  与原生沙箱“HTTPS 443 only”一致。

## 下载安全规则

下载由 Node 进程在 Codex 启动之前、在任何沙箱之外完成：

1. **地址校验**：自行解析主机名，所有解析结果必须都是公网单播地址；任一地址命中拒绝列表
   即失败。拒绝列表与 `native_tool_policy.go` 的 `deniedResolvedAddresses` 相同
   （`0.0.0.0/8`、`127.0.0.0/8`、`169.254.0.0/16`、`::/128`、`::1/128`、`fe80::/10`、
   `10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`100.64.0.0/10`、`fc00::/7`、
   `224.0.0.0/4`、`ff00::/8`），另加 `240.0.0.0/4`、`255.255.255.255/32`、`198.18.0.0/15`、
   NAT64 `64:ff9b::/96`、本地 NAT64 `64:ff9b:1::/48`（RFC 8215）与 6to4 `2002::/16`；IPv4 映射的 IPv6 先还原为 IPv4 再判断。
   IP 字面量主机同样校验。
2. **防 DNS rebinding**：自定义 `DialContext` 只连接到刚刚校验过的 IP，TLS 仍以原主机名做
   SNI 与证书校验；不使用 `HTTP(S)_PROXY` 等代理环境变量（代理会让地址校验失效）。
3. **跳转**：最多 3 次；每一跳都重新执行 URL 规则与地址校验（同一 Dial 路径），
   `https → http` 等降级直接失败。
4. **大小与超时**：单张上限 20 MiB（`Content-Length` 超限立即失败，读取时再用
   上限 +1 字节截断判定）；单张总超时 30 秒，所有图片共用 Run 的取消上下文。
5. **类型**：只接受 JPEG、PNG、WebP、GIF，**按文件头魔数判定**，响应的
   `Content-Type` 只作参考；文件扩展名按判定结果生成，不使用 URL 中的文件名。
6. **不携带凭据**：无 Cookie jar，不发送 `Authorization`、Referer 或任何 Node/Core 凭据，
   User-Agent 为固定的 `OpenLinker-Agent-Node-ImageInput/1`；只接受 HTTP 200。

下载逻辑放在新的公开叶子包 `pkg/adapters/imageinput`（只依赖标准库，不导入 Node
host/internal、Cobra 或 Plugin，不启动 Worker），Plugin 以后可按需编译期复用。

## 失败与错误码

任一图片失败时整个 Run 在启动 Codex **之前**失败（不消耗模型调用），返回
`Status: failed` 与以下错误码之一；消息只包含字段名与原因，不包含响应内容或解析出的 IP：

| 错误码 | 情况 |
|---|---|
| `IMAGE_INPUT_INVALID` | 非 https、带凭据、非 443 端口、URL 过长、图片数超过 4 |
| `IMAGE_INPUT_BLOCKED` | 主机（含任一跳转）解析到被拒绝的地址 |
| `IMAGE_INPUT_FETCH_FAILED` | 解析/连接/TLS 失败、非 200、超时、跳转过多或降级 |
| `IMAGE_INPUT_TOO_LARGE` | 超过 20 MiB |
| `IMAGE_INPUT_UNSUPPORTED_TYPE` | 文件头不是 JPEG/PNG/WebP/GIF |

Run 被取消时仍按现有 `PROVIDER_CANCELED` 处理。

## 文件位置、发送与清理

- 文件写入**本次 Run 的私有目录** `<workspace>/.openlinker-inputs/<随机名>/`，目录 0700、
  文件 0600，通过 `os.Root` 创建，工作区中的符号链接不能使写入逃逸。原生隔离时 workspace 是
  该会话作用域自己的目录；未隔离时是配置的 workspace，随机目录避免并发 Run 互相覆盖。
- 发送：`turn/start` 的 `input` 为 `[text, localImage(path)...]`。共享叶子 `codexturn.Config`
  新增可选字段 `LocalImages []string`（为空时行为不变，属于向后兼容的增量）。
- prompt 文本末尾追加一段说明：字段名 → 工作区内相对路径、类型与字节数，供模型用沙箱内
  工具（如 exiftool / c2patool，若已安装）读取元数据；原始 URL 仍保留在 `input` 中。
- 本轮的随机子目录在结束后（成功、失败、取消均）删除，且早于会话沙箱关闭；共享上级目录
  `.openlinker-inputs` 保留（空目录），避免与并发 Run 的建目录步骤竞争。
- 下载计入 Provider 超时（`OPENLINKER_AGENT_NODE_TIMEOUT_MS`）：超时或取消按现有
  超时/取消结果处理，不归类为 `IMAGE_INPUT_*`。

## 会话复用与续接

- 下载在缺失会话恢复的重试循环之外完成一次，两次尝试复用同一批文件。
- 图片只属于发送它的那一轮；下一轮续接时文件已删除，prompt 不再引用它。Codex 是否在
  rollout 中保存图片本身由 Codex 决定。2026-11-13 的客户端验收（Codex 0.153.0 与 0.163.0，
  原生隔离，合成模型端点）显示：文件删除后续接仍成功，续接请求中仍带有上一轮的 `input_image`。
  本设计不依赖该行为；沙箱工具只能在发送图片的那一轮读取文件。
- 开关不进入 `clientMode`，开启或关闭都不会作废已保存的会话。
- Core 的会话历史仍只有文本，不写入图片内容或本地路径。

## 测试计划

- `imageinput` 单元测试：私网/回环/链路本地/组播/映射地址拒绝、DNS 解析到私网拒绝、
  跳转逐跳校验（跳到私网、降级、超次数）、超大（声明与实际）、伪造类型、非 200、
  带凭据 URL、非 443 端口；用 `httptest` TLS 服务加注入的解析器/拨号器完成，不访问外网。
- 客户端验收 `TestNativeCodexImageInputInstalledClient`（设置 `OPENLINKER_TEST_NATIVE_CODEX_BIN`，CI 原生
  隔离任务必跑）：真实 Codex 客户端在原生隔离下把图片作为 `input_image` 发给模型，沙箱工具读出准确字节数，
  续接成功且会话工作区内不留图片文件。真实模型对图片内容的判断不在此验收范围。
- 适配器级测试：经 `CodexProvider.Run` 真实生产调用点与 RPC fixture，断言 `turn/start`
  同时包含文本和 `localImage`、文件在本轮可读且结束后被删除；开关关闭时请求与当前一致；
  下载失败时不启动 Codex 并返回对应错误码。
- 配置测试：开关解析与非法值。
- 默认 workspace 与 `GOWORK=off` 均执行 `go build ./...`、`go test ./...`、`go vet ./...`。

## 不在本轮范围

- **Claude 适配器**：Claude CLI 的 stream-json 输入支持 base64 图片块，但需单独设计
  （输入格式、会话恢复与 native 工具读取），列为后续。
- 发布 Node 二进制、切换本机运行中的 Node、修改 twv1 上的服务，均需另行批准。
- 文件上传字段、嵌套字段识别、更多格式（HEIC/AVIF）、图片缩放或 EXIF 剥离。
