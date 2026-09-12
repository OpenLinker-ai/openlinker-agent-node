# macOS／Linux 原生会话隔离

`OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native` 把整个 Codex／Claude Code
客户端和子工具放进系统沙箱，不依赖 Docker。macOS 使用 Seatbelt，Linux 使用
bubblewrap、PID／网络命名空间和 seccomp。客户端使用本机已安装的程序，无需打进镜像。

这是新的 Node 源码功能，尚不代表已发布二进制或运行中 Agent 已升级。默认仍为
`off`，Plugin 也不会因为引用 Node 的共享包就自动启用该策略。

## 开启方式

先安装 Node.js 20.11+、ripgrep，以及固定版本的 Anthropic sandbox runtime：

```sh
npm ci --prefix tools/native-sandbox --ignore-scripts
export OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN="$PWD/tools/native-sandbox/node_modules/.bin/srt"
```

这条命令适用于 Node 源码仓库。使用二进制安装时，可显式执行
`npm install --global @anthropic-ai/sandbox-runtime@0.0.76`，让 `srt` 位于 PATH。
Node 会核验包版本，运行任务时不会自动下载工具。

Linux 还需要 `bubblewrap`、`socat` 和内核允许的非特权命名空间；macOS 需要
`/usr/bin/sandbox-exec`。Node 必须以非 root 用户运行。任何依赖、系统能力或真实
隔离探针失败都会阻止启动，不会退回无沙箱运行，也不启用较弱的嵌套模式。

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

会话续接测试使用执行真实文件／进程操作的确定性 Codex／Claude 协议对端，并非模型
调用。真实客户端的版本和能力检查也不等于模型、WebSearch 已通过。完整技术说明及
上游来源见[英文说明](native-session-isolation.md)。
