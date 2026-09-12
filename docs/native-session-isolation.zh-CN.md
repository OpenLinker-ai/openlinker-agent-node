# macOS／Linux 原生会话隔离

**实验性功能，仅供可信调用方使用。** `ProviderConfig.SessionIsolation`、公开
`sessionsandbox` 包和 `OPENLINKER_AGENT_NODE_SESSION_*` 参数暂不承诺稳定兼容；
升级须同时核对二进制、依赖锁及适配器验收。

允许调用这个 Agent 的用户必须可信，且可以接触服务方的模型 API key。客户端或工具
读到凭据后，可以直接写入最终答复或流式 Run 事件；网络白名单挡不住这个返回通道。
从子进程环境里移除 key，也不保证它不能读取父进程的凭据。不能把当前模式开放给
不应知道该 key 的公开或不可信调用方。应在 Core 限制调用者，并使用专用且权限受限
的模型 key；Node 不会自动判断调用方是否满足这一信任条件。

`OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native` 把整个 Codex／Claude Code
客户端和子工具放进系统沙箱，不依赖 Docker。macOS 使用 Seatbelt，Linux 使用
bubblewrap、PID／网络命名空间和 seccomp。客户端使用本机已安装的程序，无需打进镜像。

这是新的 Node 源码功能，尚不代表已发布二进制或运行中 Agent 已升级。默认仍为
`off`，Plugin 也不会因为引用 Node 的共享包就自动启用该策略。

## 一个 Node 管理多个会话沙箱

对于同一个 Agent／Provider 配置，只启动一个常驻 `openlinker-agent-node`。
进程内唯一的 SDK Runtime Worker 把任务交给同一个适配器，由适配器按 Core 可信会话
作用域选择沙箱。每个聊天不需要另开 Agent Node、重新登记或创建新的 SDK DataDir。

```text
一个 Agent Node 进程（一个 SDK Runtime Worker，一种 Provider 配置）
  ├─ 会话 A → 沙箱 A → 客户端及子工具 → A 的工作区和历史
  └─ 会话 B → 沙箱 B → 客户端及子工具 → B 的工作区和历史
```

系统沙箱约束的是进程，不能给同一客户端进程内的不同会话 ID 分别设置互不相通的
文件权限。因此每个正在执行的 Run 仍会启动独立的 Codex／Claude 客户端子进程及
沙箱辅助进程；Agent Node 留在外部统一管理，不能让互不信任的会话共用一个客户端进程。

持久化的是会话数据：A 的下一轮用新客户端进程打开 A 的私有目录、恢复 A 的原生
会话 ID。Run 结束后关闭本轮沙箱，空闲会话不常驻客户端进程。
`OPENLINKER_AGENT_NODE_CAPACITY` 控制 Worker 执行容量，设为 `2` 或以上可在 Core
下发任务时并发执行不同会话。同一会话已有 Run 执行时，排他锁拒绝另一个重叠 Run；
Node 不另建任务队列或重复实现 SDK 调度。取消 A 只终止 A 本轮的普通进程组，
不会停止 Agent Node 或 B 的客户端。

单个 Node 当前选择一种适配器／Provider 配置；该 Provider 的多个聊天共享这个 Node。
本功能没有增加按聊天切换 Codex／Claude 的复合服务入口。

Plugin 已有 Provider／Browser 的容器交付；一个 Provider 容器本身并不代表每个聊天
都有独立容器。这些发布物及另存的 Node Docker 草稿均不接入本机二进制沙箱路径。

## 开启方式

先安装 Node.js 20.11+、ripgrep，以及固定版本的 Anthropic sandbox runtime：

```sh
npm ci --prefix tools/native-sandbox --ignore-scripts
export OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN="$PWD/tools/native-sandbox/node_modules/.bin/srt"
```

这条命令适用于 Node 源码仓库。由本次代码构建的二进制归档还会附带
`native-sandbox/`，含同一份 manifest、lock 和安装器。验证整个归档 SHA-256 后，
在解压目录显式执行：

```sh
sh ./native-sandbox/install.sh
export OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN="$PWD/native-sandbox/node_modules/.bin/srt"
```

安装器执行 `npm ci --ignore-scripts`，固定直接和传递依赖并验证下载完整性，不执行
生命周期脚本。旧发布包可能没有这些文件，不能混用不同版本的二进制和锁。
修改打包流程不代表新包已发布。启动时仅检查顶层包身份和版本，并不证明安装后
文件未被修改；配置的安装目录仍是可信宿主输入。Run 期间不会自动下载依赖。

Linux 还需要 `bubblewrap`、`socat` 和内核允许的非特权命名空间；macOS 需要
`/usr/bin/sandbox-exec`。Node 必须以非 root 用户运行。任何依赖、系统能力或真实
隔离探针失败都会阻止启动，不会退回无沙箱运行，也不启用较弱的嵌套模式。

Ubuntu 24.04 还可能因 AppArmor 限制用户命名空间而在 bubblewrap 启动时出现
`loopback: Failed RTM_NEWADDR: Operation not permitted`。管理员需要按 Ubuntu 的
[应用级命名空间授权说明](https://discourse.ubuntu.com/t/ubuntu-24-04-lts-noble-numbat-release-notes/39890)，
审核已有策略并明确授权发行版安装的 `/usr/bin/bwrap`。不要关闭全局限制、让 Node
以 root 运行或改用主机网络来绕过。`scripts/ci-bwrap.apparmor` 只供临时 CI 机器示范，
Node 不会自动安装策略或修改主机设置；授权启动器后，会话的文件／网络隔离仍由
bubblewrap 与 seccomp 执行。

保留已有 Node 身份和连接配置，以 Codex 为例增加：

```sh
export OPENLINKER_AGENT_NODE_ADAPTER=codex
export OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native
export OPENLINKER_AGENT_NODE_SESSION_ROOT=/srv/openlinker/native-sessions
export OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE=true
export OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS='["api.openai.com"]'
# 由服务配置提供专用 CODEX_API_KEY，勿在仓库中保存真实密钥。
```

Claude 对应设置 `ADAPTER=claude`、`CLAUDE_SESSION_REUSE=true`，联网域名为
`api.anthropic.com`，凭据用 `ANTHROPIC_API_KEY` 或符合私有文件检查的
`ANTHROPIC_API_KEY_FILE`。Claude 使用 `--bare`，不导入个人 OAuth／钥匙串登录。

`SESSION_ROOT` 的父目录应预先存在，目录由 Node 用户持有、权限为 `0700`，不能是
符号链接。已有目录不会被自动 chmod、搬迁或清空。

| 环境变量（均加 `OPENLINKER_AGENT_NODE_` 前缀） | 含义 |
| --- | --- |
| `SESSION_ISOLATION` | `off` 或 `native` |
| `SESSION_ROOT` | 持久会话数据的绝对路径 |
| `SESSION_TEMP_ROOT` | 可选私有临时目录父路径，默认 `/tmp`；解析后路径最多 40 字节 |
| `SESSION_SANDBOX_BIN` | 固定版本的 `srt`，默认从 PATH 查找 |
| `SESSION_READ_PATHS` | 额外只读代码／运行库路径的 JSON 数组，默认空 |
| `SESSION_NETWORK_DOMAINS` | 允许访问的精确公网域名 JSON 数组，只开放 HTTPS 443；默认空，即断网 |

通过 npm 安装的客户端可能还需要把客户端包目录和 Node 可执行文件加入
`SESSION_READ_PATHS`。应明确开放这些程序文件，不要开放个人 HOME、全部 `/opt`
或 `/usr/local`。启动检查也在沙箱中执行真实客户端的版本／帮助命令，缺少运行库
权限时会报错。额外只读目录是同一配置下所有会话都能读取的共享输入，不应放秘密
或其他会话的数据；会话根目录、控制目录及其祖先不能加入读取列表。

## 会话复用和数据边界

- 按 Core 地址、Provider、可信 Agent ID、Core 调用者作用域及会话键确定持久目录。
  不接受模型输入、payload／metadata 自报的身份或路径。
- 每个会话独享 workspace、HOME、CODEX_HOME、CLAUDE_CONFIG_DIR。A→B→A 时，A
  恢复自己的历史，B 使用自己的历史；更换 Run、Runtime Session 或 epoch 不切断续接。
- 会话映射、跨进程锁和生成的沙箱策略留在客户端不可读的控制目录。
- 其他会话及主机私有文件不在读取列表内，子进程、符号链接和硬链接也必须遵守边界。
  macOS 额外禁止个人偏好设置、钥匙串 Mach 服务及 POSIX 共享内存／信号量通道。
- 不继承平台 token、SSH agent、个人代理设置或任意环境变量。网络代理按域名检查，
  阻止回环、主机本地、私网等地址与直接联网绕过。

开启后会建立新的受保护原生会话，由 Core 历史初始化；不会自动导入旧映射、个人
历史或共享工作目录。旧 `SESSION_STORE` 覆盖、任意 `ENV_ALLOWLIST`、模拟响应及
宿主委派 socket 都不能与该模式混用。Browser 仍属于 Plugin，未在此入口中接入。

Codex 内层显示 `danger-full-access` 是为了避免嵌套沙箱；仅在外层系统沙箱已构造后
才使用该参数，整个 app-server 及工具仍受外层约束。结果另有
`session_isolation=native` 字段说明实际执行方式。联网域名配置不会自动开启 WebSearch，
仍须单独配置 Provider 的工具开关与权限。

## 限定范围

持久目录和每轮临时数据均没有 Node 强制执行的字节／inode 配额。默认每轮可写
`/tmp/olns-*`；单个调用方可以耗尽宿主文件系统，`/tmp` 为 tmpfs 时还可能挤占内存。
目录私有、任务超时和退出后清理，都不能防止运行期间耗尽资源。

可设置 `OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT`，使用管理员事先配置了配额的
独立文件系统目录。其父目录需存在，目录必须私有且非符号链接，解析后路径最多
40 字节以容纳 Unix socket 名称。Node 在 srt 包装后、启动客户端时恢复私有
TMPDIR／TMP／TEMP，确保客户端实际使用所选目录。结束时只删除本轮子目录。

迁移目录不等于容量限制；持久和临时数据都需要底层存储实施限额。共享分区限额
可以保护宿主其他存储，但不能防止一个会话挤占该分区中的其他会话。每会话硬配额
仍未实现，不能据此开放给对抗性调用方；Node 不会自动挂载、设置配额或修改主机策略。

关键回环、未指定地址、链路本地／元数据、私网和组播范围已在 Node 中显式拒绝。
Linux 外层启动命令严格解析为 argv 后直接执行 bubblewrap，不再经宿主 `bash -c`；
沙箱内的代理／seccomp 脚本仍由固定版本 srt 生成，未知格式或缺少命名空间会停止执行。

同一系统用户下的所有不可信会话都必须启用隔离。一个会话的沙箱无法限制另一个仍以
普通进程运行的会话，也不限制可信宿主程序、Node 自身、管理员或内核。模型客户端
仍能访问自己使用的专用模型凭据，此功能不提供对客户端隐匿凭据的代理。

目前不提供每会话 CPU、内存、持久磁盘及 inode 配额。取消覆盖普通进程组后代；
自行 daemonize 的进程及宿主崩溃后的孤儿回收，不应宣称具有容器级保证。

真实 OS 验收入口：

```sh
export OPENLINKER_TEST_NATIVE_SANDBOX_BIN="$PWD/tools/native-sandbox/node_modules/.bin/srt"
bash scripts/test-native-session-isolation.sh
```

测试通过同一个 Node 的生产 Runtime handler 并发执行两个会话，检查同会话排他、
单独取消和续接；另外的 Node 侧进程重启测试用于验证恢复，不代表每个会话要启动一个
Node。测试使用执行真实文件／进程操作的确定性 Codex／Claude 协议对端，不覆盖 Core
网络传输和调度，也不是模型调用。真实客户端的版本和能力检查也不等于模型、WebSearch
已通过。完整技术说明及
上游来源见[英文说明](native-session-isolation.md)。
