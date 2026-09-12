# Codex / Claude 会话与宿主隔离

`SESSION_ISOLATION=docker` 把整个原生客户端及其子进程放进 Linux 容器。
每轮重新启动容器，同一个 Core 会话的数据目录持久保留，Node 重启后仍可续接。
默认关闭；关闭时保留原有启动方式、默认值和个人账号登录行为。

Codex 通过共享 `codexturn` 的命令工厂接入容器，复用相同的 RPC、取消和退出清理；
隔离层只负责容器及会话目录。该功能独立于已发布的 Node 委派修复，合并前需单独审查安全模型。

## 启用参数

以下变量统一加 `OPENLINKER_AGENT_NODE_` 前缀：

| 后缀 | 含义 |
| --- | --- |
| `SESSION_ISOLATION` | `off`（默认）或 `docker`；失败不会退回宿主执行 |
| `SESSION_ROOT` | Node 专有的绝对路径，只允许所属用户访问；父目录需提前准备 |
| `SESSION_IMAGE` | 本机已安装的 Linux 镜像，使用完整 `sha256:...` ID 或 `仓库@sha256:...`；拒绝浮动标签和声明额外卷的镜像 |
| `SESSION_NETWORK` | 默认 `none`；可显式选择已存在的 Docker bridge 网络 |
| `CODEX_SESSION_REUSE` / `CLAUDE_SESSION_REUSE` | 选中客户端的对应变量必须明确设为 `true` |
| `CODEX_BIN` / `CLAUDE_BIN` | **镜像内**的客户端路径，例如 `/usr/local/bin/codex` |

示例（需要替换成已安装镜像的真实摘要）：

```sh
OPENLINKER_AGENT_NODE_ADAPTER=codex
OPENLINKER_AGENT_NODE_SESSION_ISOLATION=docker
OPENLINKER_AGENT_NODE_SESSION_ROOT=/srv/openlinker/native-sessions
OPENLINKER_AGENT_NODE_SESSION_IMAGE='your-client-image@sha256:<64位摘要>'
OPENLINKER_AGENT_NODE_SESSION_NETWORK=none
OPENLINKER_AGENT_NODE_CODEX_BIN=/usr/local/bin/codex
OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE=true
```

继续配置原来的 Core 地址、Agent 身份和独立的 SDK `DATA_DIR`，并向 Node 注入专用
`CODEX_API_KEY`。Claude 改为 `ADAPTER=claude`，配置 `CLAUDE_BIN`、
`CLAUDE_SESSION_REUSE=true`，凭据使用 `ANTHROPIC_API_KEY`。

Node 必须以非 root 系统用户运行，使用 macOS Docker Desktop 或 Linux 本机的
Unix socket Docker 上下文。容器 UID/GID 与 Node 用户一致，Docker 的用户映射须允许
该身份写入挂载目录。此模式尚不支持 Windows Node 和远程 Docker daemon。
镜像应包含 Linux 客户端及其依赖；不会复制 Mac 可执行文件，也不会在收到任务时拉镜像。
启动前会在容器中检查客户端版本和帮助参数。

默认 `none` 完全断网，适合离线验收，**不能调用模型 API 或执行网络搜索**。
实际联网运行需明确选择运维配置好的 bridge 网络，并设置出口规则：放行模型服务和
获准的 Web 访问，按需阻断宿主服务、内网和其他会话的端点。仅选择 `bridge` 本身不提供
网络白名单。拒绝 host 网络、共用其他容器网络以及任意 Docker 参数透传。
Claude 原有工具开关、允许列表和权限模式继续生效。

## 会话如何持久、互相隔离

目录由 Core 连接命名空间、客户端类型、可信 Agent ID、SDK 校验后的调用者作用域和
Core 会话 key 共同生成。当前 Run ID 也必须与 Core 会话上下文匹配。任务正文不能指定
目录或原生会话 ID；不同调用者、Agent、Core、客户端或会话均使用不同目录。
Runtime 连接 Session/epoch 轮换不改变这个持久会话。

```text
SESSION_ROOT/<作用域哈希>/
  session.lock          # Node 专用，跨进程排他锁
  docker-engine         # Node 专用，绑定执行容器所属 daemon
  native-session.json   # Node 专用，原生会话 ID 与历史同步游标
  data/                 # 唯一挂载目录，容器内为 /session
    workspace/          # 本会话的持久工作文件
    home/               # 独立 HOME
    codex/              # 独立 CODEX_HOME、原生记录
    claude/             # 独立 CLAUDE_CONFIG_DIR、原生记录
```

Codex 创建非临时 thread，后续 `thread/resume`；Claude 保留原生记录，后续 `--resume`。
容器结束不会删除会话数据。私有映射、SDK spool、平台令牌、其他会话、个人主目录和
Docker socket 都不挂进容器。程序镜像只读，以非 root 身份运行，禁止提权，限制 CPU、
内存、进程数量，并使用独立进程和 IPC 命名空间。

此模式下 Codex **内部**配置为 `danger-full-access`、approval `never`，实际隔离由外层
Docker 提供；不会为了套第二层沙箱而放宽容器权限。同一会话并发占用会报 busy。
取消会清理整个容器；Node 异常退出后，下次使用该会话前先清理同作用域、同所有权标记
的残留容器。清理失败会报错，不返回成功。

会话还绑定 Docker daemon 身份，切换到其他 daemon 会被拒绝，避免旧容器还在写、
新 daemon 又挂载同一份数据。此时应恢复原上下文；跨 daemon 迁移需另行明确处理。

当前没有自动删除策略，会话数据持续保存在本机；此功能不加密这些文件，受信任的宿主
管理员和 Docker 管理者仍可访问。专门注入的模型凭据可被该容器内客户端读取；尚无
让模型凭据对模型进程不可见的凭据代理。

## 兼容范围与验收

启用时删除原来 `*_WORKSPACE`、`*_SESSION_STORE` 的配置，Codex 还需移除单独的
`*_SANDBOX`、`*_APPROVAL` 和 mock 配置。新工作目录起初为空，由 Core 可信历史补齐
上下文；不会导入、搬动或清理旧原生记录、旧工作目录和 SDK spool。SDK `DATA_DIR`
与 `SESSION_ROOT` 必须互不包含。续接需要命名空间、会话根目录及原生数据保持稳定。

这版不导入个人 Codex OAuth 文件，也不导入 Claude 钥匙串/OAuth；需要专用 API 凭据。
宿主 MCP 委托暂时拒绝启用，后续需要独立设计作用域受限的传输方式。不能用共享 HOME、
挂载宿主目录、特权容器或 Docker socket 绕过限制。Provider 镜像的打包归原有交付仓负责。

运行 `sh scripts/test-session-isolation.sh` 可执行不调用模型的真实容器验收：两种协议、
独立 Node 进程间的 A→B→A 续接、跨调用者/Agent/Core 隔离、宿主文件和符号链接读取探针、
映射文件隔离、跨进程锁、取消子进程、异常残留容器恢复。已加入 CI。
协议夹具不是真实模型；可选的 `TestDockerSessionNativeCompatibility` 检查真实客户端
版本和帮助参数。真实模型工具调用、账号认证以及具体部署环境仍需另外验收。

英文说明及官方契约链接见 [session-isolation.md](session-isolation.md)。
