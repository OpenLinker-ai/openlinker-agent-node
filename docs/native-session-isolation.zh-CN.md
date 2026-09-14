# 复用主机认证的原生会话隔离

**实验性，默认关闭，支持 macOS 与 Linux。** Node 调用主机已安装的 Codex 或
Claude Code。请先用运行 Node 的同一系统用户，按官方方式登录或配置客户端。
开启 `SESSION_ISOLATION=native` 不需要在每个沙箱重新登录，也不强制提供新的
`CODEX_API_KEY` / `ANTHROPIC_API_KEY`。Node 不读取、复制、刷新或代理订阅 token。

## 认证与工具的边界

官方客户端是可信宿主进程，继续负责自己的认证；模型控制的工具单独受限制：

- Codex 使用命名文件权限配置和系统沙箱，工具使用私有 HOME 与精简环境。
  新建及恢复 thread 都不再传旧的 sandbox 覆盖参数，避免覆盖精确文件权限。
  明确关闭客户端内执行的 `view_image` 和主机配置的 `notify` 命令，即使用户配置开启它们。
- Claude 保留原来的 HOME / CLAUDE_CONFIG_DIR，使用 `--safe-mode --restricted`，
  不使用会跳过订阅认证的 `--bare`。**本地工具默认只开 Bash**；显式配置才能开启
  Read/Edit/Write/Glob/Grep，它们受客户端权限检查，不在 OS 沙箱中。
  Bash 使用客户端系统沙箱，要求初始化成功、禁止排除命令和无沙箱重试。
  命令环境始终禁用凭据：macOS 另开全局子进程清理，Linux 使用常规 Bash 沙箱的
  credential deny 规则和 PID/proc 命名空间。Claude 2.1.259 的全局清理开关会额外
  放宽 Linux 可写目录，本模式在 Linux 明确关闭该开关。
  `CLAUDE_CODE_TMPDIR` 也设为本轮私有临时目录，避免命令收尾记录使用公共 `/tmp/claude-<uid>`。
- 工具不能获得认证目录、其他会话或 Node 控制文件的读取授权；不会继承平台 token
  和环境注入选项。个人插件、MCP、hooks、跨会话与桌面/浏览器入口不接入此模式。
  网络白名单不能防止可读凭据通过输出泄露，因此凭据保护依靠文件和进程边界。

官方二进制、系统用户/管理员、管理员下发的客户端策略和显式允许的代码路径仍然可信。
这不是防御恶意宿主进程、被篡改客户端或主动放宽沙箱的管理员，也不保证任意客户端
版本都安全。实验阶段继续只对可信调用方开放。不要把个人订阅直接作为共享公共服务；
账号使用权限、供应商条款、费用限额和平台访问控制仍需单独满足。

## 会话复用

一个 Node 可同时处理多个会话，每个活跃 Run 启动对应客户端进程，其工具在各自沙箱
运行；空闲会话保留数据，无需常驻客户端。Core 提供的可信 principal、Agent、会话
key 与 Core 命名空间共同决定存储位置，不接受 payload 自报身份。跨进程锁防止同一
会话并发写入，取消 A 不会停止 B。

A → B → A 会恢复 A。不会把主机个人聊天历史导入会话。Codex 使用受控的 thread ID；
Claude 另用 `CLAUDE_CODE_PROJECT_DIR_NAME` 分开项目记录，同时保持认证目录不变。

## 配置

已验证的客户端基线为 Codex 0.153.0、Claude Code 2.1.259。Linux 需要可用的非特权
user/network/PID namespaces，Claude 另需 `bwrap`、`socat`、`rg`。macOS 使用客户端
内建 Seatbelt 沙箱。Node 的此模式不再要求另装 npm SRT 包。
认证与会话目录必须放在系统/代码只读授权之外。Linux Codex 还需读取当前进程的
辅助程序别名目录和可执行文件；Node 先确认目录只有已知别名及空锁文件，再只读
开放这些代码路径，不开放认证目录或整个 tmp 树。

```sh
export OPENLINKER_AGENT_NODE_ADAPTER=codex # 或 claude
export OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native
export OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE=true # Claude 改为 CLAUDE_SESSION_REUSE
export OPENLINKER_AGENT_NODE_SESSION_ROOT=/absolute/private/node-sessions
# 保留原来的 HOME，以及已配置的 CODEX_HOME / CLAUDE_CONFIG_DIR。
# 已有客户端登录时，无需增加 API key 环境变量。
```

`SESSION_ROOT` 必须为当前用户独占的目录，不能以 root 运行。以下选项都加
`OPENLINKER_AGENT_NODE_` 前缀：

| 参数 | 含义 |
| --- | --- |
| `SESSION_ISOLATION` | 默认 `off`，选择 `native` 开启 |
| `SESSION_ROOT` | 私有持久目录 |
| `SESSION_TEMP_ROOT` | 可选私有临时目录，解析后路径不超过 40 字节 |
| `SESSION_READ_PATHS` | 为 shell 额外开放的只读代码/库路径 JSON 数组；不能暴露认证、会话和控制目录 |
| `SESSION_NETWORK_DOMAINS` | 沙箱命令可访问的精确公网 HTTPS 主机名 JSON 数组，空数组表示命令断网 |
| `CLAUDE_ALLOWED_TOOLS` | 未设置/空数组默认 Bash；可信任务可用显式 JSON 数组选择 Read、Edit、Write、Glob、Grep、Bash，注意下方文件工具边界 |
| `CODEX_WEB_SEARCH` / `CLAUDE_WEB_SEARCH` | 单独控制客户端原生搜索工具 |

Codex 保留既有模型 Provider 配置，不再强制改到默认 API 地址。Claude 复用缓存认证
或标准凭据环境及 `ANTHROPIC_BASE_URL`。显式的 `CODEX_BASE_URL` /
`CLAUDE_BASE_URL` 覆盖仍支持 HTTPS 443 和完整 API 路径；Codex 显式网关沿用可选的
`CODEX_API_KEY`。这不会使普通订阅登录变成必须提供 key。

域名列表仅限制**沙箱命令**联网，不限制可信客户端模型请求、认证与服务端 WebSearch。
配置网关不会自动给命令开放网关权限，也不会关闭 TLS 验证。Claude restricted 模式
忽略用户/项目自定义设置，依赖 settings 内 helper 或云厂商认证的特殊配置还需另行
验证，不能自动提取订阅 token 或改为代理。

## 迁移与验收

### 显式开启文件工具的边界

账号敏感场景保留默认值；Bash 仍可在会话目录内读写、搜索文件。
`CLAUDE_WEB_SEARCH` 独立控制，不会随文件工具默认值变更而开启。

不要由宿主向会话目录放入敏感文件的硬链接。硬链接是同一个 inode 的另一个名字，
不能靠 realpath 判断其原始路径。真实客户端测试分别验证沙箱 Bash 创建的链接，
以及可信测试宿主预置的硬链接；显式开启的 Read/Grep 和沙箱 Bash 都可以读取后者。
只开 Bash 不会撤回宿主已暴露的 inode。因此符号链接
测试通过不等于任意 inode 别名都安全。关闭工具不会擦除已有客户端记录和 Core 历史；
收紧权限后，不应继续复用可能已包含凭据的会话。

新增矩阵覆盖五个文件工具、文件/目录符号链接、硬链接，以及 Linux `/proc/self`
别名。正向对照确认工具实际执行；检查宿主原文件，区分原子替换链接与修改原 inode。
默认模式还主动提交未注册的文件工具调用，要求拒绝。结论仅覆盖固定版本与具体用例，
不代表链接切换竞态或所有客户端文件/IPC 接口已被证明安全。

### 旧 SRT 会话

旧实现把整个客户端放入 SRT 沙箱，会改认证目录并要求专用 key。新实现使用独立的
host-auth 存储 scope，不会复制或删除旧工作区、历史或可能包含凭据的数据。因此迁移
后第一次会新建会话，后续继续复用。删除旧的 `SESSION_SANDBOX_BIN`，保留会报错，
不会静默忽略。不能混用旧 `SESSION_STORE`、委派 socket 和任意 `ENV_ALLOWLIST`。

启动检查客户端能力和系统依赖；macOS Codex 还实际验证允许写入/拒绝读取，Linux
检查 user/network/PID 命名空间可用。Claude 在开始
任务时由 `failIfUnavailable` 强制初始化沙箱，版本/help 检查本身不能证明文件隔离。
任何失败都不允许自动退回无沙箱运行。

`test-native-session-isolation.sh` 使用真实客户端、临时合成登录和本地固定模型响应，
分别检查显式开启的文件工具、默认 Bash、A-B-A、文件工具链接矩阵、跨会话读取、越界写入、环境与父进程
凭据读取；macOS 还使用明确指定的临时钥匙串测试合成密码，不读取登录钥匙串。不会消耗
真实账号额度；这不代表真实模型、WebSearch 或账号合规已完成验收。CI 覆盖两套系统。

Linux 的空白 tmpfs 覆盖层可能允许创建同名临时文件，验收以宿主真实认证文件和
目录未被修改为准；写入隔离覆盖层不等于获得宿主写权限。

**尚无会话级磁盘、内存、进程数硬配额。** 临时目录设置和超时不能替代配额。即使
读不到凭据，任务仍可能消耗磁盘或账号预算。详见[跟踪记录](native-session-isolation-follow-ups.md)。
