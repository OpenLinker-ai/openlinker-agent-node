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

下载逻辑放在新的公开叶子包 `pkg/adapters/imageinput`（只依赖标准库与已在模块图中的 `golang.org/x/net`，不导入 Node
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

## 部署注意：fake-IP DNS

若主机使用 TUN 代理的 fake-IP 模式（系统 DNS 把公网域名解析到 `198.18.0.0/15`），所有链接都会被
正确判为 `IMAGE_INPUT_BLOCKED`，功能在该主机上不可用。2026-11-13 已在用户本机（运行 launchd Node
的 Mac）确认此情况：系统 DNS 与直连 UDP 53 均返回 `198.18.x.x`，经代理的 DoH 返回真实地址，直连真实
地址可达。本 PR 不放宽地址规则，而是增加可选 DoH（2026-11-13 用户批准）：

- `OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_DOH_URL`，默认不设置（使用主机 DNS），须同时开启图片输入。
- 只接受 443 端口的 `https://<公网 IP>/<路径>`，不含凭据、查询或片段；IP 本身须通过同一拒绝列表。
- RFC 8484 POST `application/dns-message`，分别查询 A 与 AAAA；只连接配置的 IP，不走代理、不跟随跳转；
  单次查询 5 秒、响应 64 KiB 上限；要求响应位、ID 为 0、问题与查询一致、未截断，SERVFAIL 等失败；
  任一查询失败即整体失败，**不回退主机 DNS**。
- DoH 只提供候选地址：仍逐个校验并固定连接，返回私网或 fake-IP 地址照样 `IMAGE_INPUT_BLOCKED`。
- 代价：图片主机名会发送给该解析器；解析器不可达时下载失败（`IMAGE_INPUT_FETCH_FAILED`）。
- 本机实测：`https://223.5.5.5/dns-query` 与 `https://1.1.1.1/dns-query` 均返回真实地址并完成下载，
  不配置 DoH 时同一链接被拦截。

## 内联图片（本地上传，2026-11-13 批准方案 A）

- 同一字段约定下，值也可以是 `data:image/(jpeg|png|webp|gif);base64,<数据>`（不允许其他参数）。
- 先校验再落盘：base64 合法、解码后不超过上限（编码长度超限时不解码直接拒绝）、文件头须与声明类型一致；
  失败码同上（`IMAGE_INPUT_INVALID` / `_TOO_LARGE` / `_UNSUPPORTED_TYPE`）。不访问网络，可信来源限定模式下也可用。
- 开启开关时，prompt 中的 `input`、`metadata`、`a2a` 与会话历史里任何内联图片 data URL 都替换为占位说明，
  图片只以 `localImage` 发送；关闭开关时行为不变（内联数据会作为文本进入 prompt，故使用图片字段的
  服务必须开启开关）。
- 订单字段类型、Web 选择器与大小上限由 Cloud/Web 负责，见主仓
  `docs/superpowers/specs/2026-11-13-local-image-upload-design.md`。

## 私有部署：可信图片来源（2026-11-13 用户批准）

图片多来自部署方自有存储时，不放宽默认规则，而是由部署方列出可信来源：

- `OPENLINKER_AGENT_NODE_CODEX_IMAGE_INPUT_TRUSTED_SOURCES`：JSON 数组，元素为精确的
  `http(s)://主机[:端口]`（不支持通配符、路径、凭据）；协议、主机、端口须完全一致，每一跳都单独判断。
- 可信来源可使用 http、任意端口与私网/回环地址，经主机 DNS 解析（不走 DoH，内网域名才能解析）；
  链路本地（含云元数据 169.254/fe80）、组播、未指定与广播地址仍拒绝。
- 非可信链接仍按公网规则；`..._TRUSTED_ONLY=true` 时一律 `IMAGE_INPUT_BLOCKED`（须同时配置来源）。
  可信来源跳转到非可信地址时按非可信规则判断。
- `..._CA_FILE`：PEM 根证书追加到系统根证书，用于企业内部 CA；作用于所有图片请求的 TLS 校验。
- 理由：Node 无法分辨链接是否由部署方"主动发送"，任何能触发 Run 的调用方（含 A2A、委派中由模型
  生成的输入）都能填写链接；放开内网后模型会描述所读内容，等于把内网内容回传给调用方。
- 本地上传：当前服务表单只有 select/number/text/url 字段，Node 只收到 Run 输入 JSON，因此仍须是链接。
  直接上传需 Web 上传字段、Core 存储与可信下发通道（Core 的 A2A 层已能规范化 file part），属于 Core/Web
  的单独设计，不在本 PR。

## 证书与凭据校验（2026-11-13 审计与加固）

- 图片与 DoH 请求都由 Go TLS 按请求主机（每一跳）校验证书链与主机名/IP SAN，使用系统根证书。
- 加固：`Fetcher.TLS`/`DoHResolver.TLS` 只能追加信任根（测试用）；`InsecureSkipVerify`、`ServerName`
  覆盖、客户端证书（防止把 Node 的 mTLS 身份发给图床）、`KeyLogWriter` 一律重置，最低 TLS 1.2，禁止重协商。
- 测试覆盖：主机名不匹配、不受信任 CA、传入 `InsecureSkipVerify` 或 `ServerName` 均失败；服务器请求
  客户端证书时不出示；DoH 证书来自不受信任 CA 或属于其他 IP 时失败。去掉加固后对应测试会失败。
- 凭据：不使用 Cookie、不发送 Authorization/Referer、拒绝带用户信息的链接（含跳转）、不读取代理环境变量；
  请求头只有固定 User-Agent 与 Accept。未做 OCSP/CRL 吊销检查（Go 默认行为）。

## 实机模型验收（2026-11-13）

非隔离 Node 生产调用点 + 用户自有 Codex 登录（OpenAI 兼容路由，`gpt-6.1-sol`），一张维基共享资源的
真实猫照片（解析器为本次手工固定到 DoH 返回的公网地址，其余均为生产代码）：Node 下载并以
`localImage` 发送；模型正确描述图片内容并给出"较可能为真实照片、视觉检查不能证明真实性"的判断；
沙箱内 `file` 读出 1600×1598 与 EXIF 信息；本轮目录随后删除。未在原生隔离下用真实模型运行。

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
