# Persistent isolated Codex and Claude sessions

Chinese: [会话与宿主隔离](session-isolation.zh-CN.md).

`SESSION_ISOLATION=docker` runs the **whole native client and its descendants**
in a fresh Linux container on each turn, while retaining a private directory for
that Core conversation. A new Node process resumes the same native session.
This feature is opt-in; the existing native launch defaults and personal login
behavior are unchanged when it is off.

Codex supplies a container command factory to the shared `codexturn` lifecycle;
RPC, cancellation and shutdown use the same implementation as native execution.
This feature is separate from the released delegation fixes and requires its
own security review before merging.

## Configuration

All variables in this table have the `OPENLINKER_AGENT_NODE_` prefix.

| Suffix | Value / behavior |
| --- | --- |
| `SESSION_ISOLATION` | `off` (default) or `docker`; no automatic fallback |
| `SESSION_ROOT` | Absolute, owner-only Node directory; its parent must already exist |
| `SESSION_IMAGE` | Already installed Linux image, selected by `sha256:...` ID or `repository@sha256:...` digest; mutable tags and image-declared volumes are rejected |
| `SESSION_NETWORK` | `none` by default; optionally an existing, explicitly selected Docker bridge network |
| `CODEX_SESSION_REUSE` / `CLAUDE_SESSION_REUSE` | Must be explicitly `true` for the selected provider |
| `CODEX_BIN` / `CLAUDE_BIN` | Client executable **inside the image**, e.g. `/usr/local/bin/codex` |

Use a non-root Node process and a local Unix-socket Docker context on macOS
(Docker Desktop) or Linux. The container UID/GID match the Node OS user; the
daemon's user mapping must allow that identity to write the bind mount. Windows
Node hosts and remote Docker daemons are not supported by this mode. The image
must contain the Linux client and its dependencies. No host executable is copied,
and Node never pulls an image during an assignment. Startup verifies native
version/help in a container (Codex >= 0.153.0, Claude >= 2.1.259).

For example, select an installed image digest and provision a private parent
directory before starting Node:

```sh
OPENLINKER_AGENT_NODE_ADAPTER=codex
OPENLINKER_AGENT_NODE_SESSION_ISOLATION=docker
OPENLINKER_AGENT_NODE_SESSION_ROOT=/srv/openlinker/native-sessions
OPENLINKER_AGENT_NODE_SESSION_IMAGE='your-client-image@sha256:<64-hex-digest>'
OPENLINKER_AGENT_NODE_SESSION_NETWORK=none
OPENLINKER_AGENT_NODE_CODEX_BIN=/usr/local/bin/codex
OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE=true
```

Retain the usual Core connection, Agent identity and **separate** SDK DataDir
configuration. Supply a dedicated `CODEX_API_KEY` to Node. For Claude, select
`ADAPTER=claude`, `CLAUDE_BIN` and `CLAUDE_SESSION_REUSE=true`, and supply
`ANTHROPIC_API_KEY`. The Node API-key environment is never printed by the Docker
launcher. Container clients can read the model credentials injected into their
own process; this mode does not implement a model-credential proxy.

`none` is useful for offline acceptance and prevents model API calls and web
tools from reaching the network. For online execution, explicitly select a
bridge network with an operator-managed egress policy. Allow the required model
endpoints and optional web access; block host services, private networks and
other session endpoints as needed. Selecting `bridge` alone does **not** provide
an egress allowlist. Host/container network sharing and arbitrary Docker flags
are not accepted. Claude's existing web-tool and permission settings still apply.

## What persists and what is isolated

Scope is length-framed and hashed from the configured Core namespace, provider,
trusted Agent ID, SDK-authoritative principal scope and Core session key. It
requires the current Core Run ID to match the invocation. Task input cannot
choose a private directory or a native session ID. A different caller, Agent,
Core installation, provider or conversation receives different state. A Runtime
Session/epoch rotation does not reset the native conversation.

The only host mount is the selected conversation's generated `data` directory:

```text
SESSION_ROOT/<scope>/
  session.lock          # Node only; cross-process exclusive ownership
  docker-engine         # Node only; pins the daemon used for orphan fencing
  native-session.json   # Node only; native ID and history synchronization cursor
  data/                 # the sole bind mount, at /session
    workspace/          # persistent working files
    home/               # private HOME
    codex/              # private CODEX_HOME and native rollouts
    claude/             # private CLAUDE_CONFIG_DIR and native sessions
```

Codex starts a persistent thread and resumes its saved thread ID; Claude persists
its transcript and uses `--resume`. The container is disposable, not the data.
Private maps, SDK state, platform tokens, other conversations, personal home
directories and the Docker socket are never mounted. Read-only image files and
standard container devices remain available. Containers run without capabilities
or privilege escalation, with a private PID/IPC namespace and bounded CPU,
memory and process counts. Codex's *inner* sandbox is `danger-full-access` with
approval `never` in this mode: **Docker is the outer boundary**. Node does not
enable a nested sandbox by relaxing the container's capability/seccomp policy.

Concurrent use of the same scope is rejected as busy. Cancellation removes the
whole container. On the next turn after a Node crash, the lock owner removes any
leftover container with the exact matching scope/ownership label before reading
native state. Container cleanup failure is an error, not a successful result.
No automatic retention/expiry deletes session data. These local files are not
encrypted by this feature; the trusted host administrator/Docker daemon can
access them.

Each scope is also bound to its Docker daemon identity. Switching contexts to a
different daemon is rejected, so an old container cannot keep writing while a
new daemon mounts the same data. Restore the original context; moving persistent
sessions between daemons requires an explicit migration outside this mode.

## Compatibility and verification

Remove legacy provider `*_WORKSPACE` and `*_SESSION_STORE` overrides when opting
in. The new workspace starts empty, seeded with trusted Core conversation
history; old native maps/workspaces and SDK spool are not copied, moved or
deleted. Keep namespace, session root and native state stable for reuse.

Personal Codex OAuth files and Claude keychain/OAuth login are **not imported**.
Host MCP delegation is rejected in this first version, because mounting its
socket would require a separate scoped transport design. Do not bypass that
rejection with a shared HOME, host mount, privileged container, or Docker socket.
Provider image packaging remains outside this Node implementation.

Run `sh scripts/test-session-isolation.sh` for real container acceptance without
model calls: both protocols, separate Node processes, A→B→A resume, caller/Agent/
Core separation, host-file and symlink probes, native-private mappings,
cross-process locking, cancellation of descendants and orphan recovery. CI runs
this command. The fixture is a protocol peer, not a model. Optional existing
native images can also be checked with `TestDockerSessionNativeCompatibility`;
those checks only validate version/help. Live model/tool execution and personal
account authentication are separate acceptance steps.

Native contracts: [Codex App Server](https://learn.chatgpt.com/docs/app-server),
[Claude CLI](https://code.claude.com/docs/en/cli-reference).
Container controls: [Docker run reference](https://docs.docker.com/reference/cli/docker/container/run/).
