# Locked optional native sandbox dependencies

For experimental native mode on macOS/Linux only. The archive's SHA-256 covers
this manifest, lock and installer along with the Node binary. Keep these files
from the same verified release together. Node.js 20.11+, ripgrep and the OS
sandbox prerequisites are still installed separately.

From this directory, explicitly run:

```sh
sh ./install.sh
export OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN="$PWD/node_modules/.bin/srt"
```

The installer uses `npm ci --ignore-scripts`, installing the exact direct and
transitive versions and checking the lock's tarball integrity. It does not use a
global npm install, resolve new compatible dependency versions, start an Agent,
or modify system security policy. Runtime startup checks the top-level package
identity/version; it is not an attestation of a subsequently modified install.
The configured installation and host administrators remain trusted.

Do not use native mode for callers who must not receive the provider API key.
Network filtering cannot prevent credentials from returning in Run output.
The mode does not impose per-session disk, temporary-storage or memory quotas.
See `../docs/native-session-isolation.md` in binary archives for full boundaries.

中文：这是 macOS/Linux 实验性原生沙箱的可选依赖包。先验证整个二进制归档的
SHA-256，再在本目录显式执行 `sh ./install.sh`。安装器用相邻锁文件执行
`npm ci --ignore-scripts`，固定直接和传递依赖并验证下载完整性，不自动启动 Agent。
不同版本的安装器、锁文件和二进制不能混用；启动检查不证明安装后文件未被修改。
调用方必须可信且可以接触模型 key；Run 输出不受网络白名单控制，也没有每会话资源配额。
