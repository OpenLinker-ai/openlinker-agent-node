# Native sessions with host authentication

**Experimental, opt-in, macOS and Linux.** Node bridges the installed Codex or
Claude Code client. Authenticate/configure that client once as the OS user that
runs Node. `SESSION_ISOLATION=native` does not require another login or a new API
key. Node does not parse, copy, refresh or proxy subscription tokens.

## Boundary

The official client remains a trusted host process and owns authentication.
Its model-controlled tools are restricted separately:

- Codex uses a named filesystem permission profile, a clean tool environment,
  and its native OS sandbox. Legacy thread/start and thread/resume sandbox
  overrides are omitted so they cannot replace that profile.
- Claude retains its normal authentication home, uses `--safe-mode --restricted`
  (not `--bare`), a strict empty MCP configuration and an explicit tool set.
  Read/Edit/Write/Glob/Grep use client permission checks. Bash uses the built-in
  OS sandbox with `failIfUnavailable=true`, no excluded commands and no
  unsandboxed retry. Credentials are denied to command environments. macOS also
  enables global subprocess scrubbing; Linux uses the normal Bash sandbox's
  credential deny rules and PID/proc namespaces. The global scrub switch in
  Claude 2.1.259 adds broad Linux writable roots and must not be enabled here.
- Authentication directories, other sessions and Node control files are outside
  tool grants. Shells receive no platform token or ambient loader configuration.
  Codex tool HOME is private; Claude keeps its auth home in the trusted client
  and denies it to tools. A network filter is not used as a substitute for
  preventing readable credentials from returning in Run output.
- Host plugins, custom MCP servers, hooks and cross-session/desktop/browser
  tools are not part of this mode. Node does not implement another model loop.

The official binary, OS user/admin, client settings delivered by an administrator
and allowed code/library paths are trusted. This is not isolation from a hostile
host process, compromised official client or administrator policy that weakens
its sandbox. It is not a blanket claim that arbitrary client versions are safe.
Do not expose an operator's personal subscription as a shared public service.
Account authorization, provider terms, spending limits and access control remain
separate requirements. Use with trusted callers while this feature is experimental.

## Session lifetime

One Node serves multiple conversations. Each active Run starts its client process;
its tools execute in that session's sandbox. Idle sessions retain data without
keeping client processes alive. Core's trusted principal, Agent, conversation
key and Node's Core namespace select the storage scope. Payload fields cannot
select a native session. A cross-process lock prevents overlapping writes to the
same conversation; cancellation of A does not stop B.

A → B → A resumes A's native session. Host personal transcripts are not imported
as conversation history. Codex controls the resumed thread ID; Claude additionally
partitions its project transcripts using `CLAUDE_CODE_PROJECT_DIR_NAME`, while
keeping `CLAUDE_CONFIG_DIR` unchanged for authentication.

## Setup

Install supported official clients (tested baseline: Codex 0.153.0 and Claude Code
2.1.259). Configure/login to the selected client normally **as the Node OS user**.
Linux needs functioning unprivileged user/network/PID namespaces; Claude also
needs `bwrap`, `socat` and `rg`. macOS uses the client's Seatbelt sandbox.
A separate npm installation of SRT is no longer required for Node's native mode.
Authentication/session directories must be outside system/code read roots.
On Linux Codex needs its per-process helper-alias directory and executable;
Node verifies that the directory contains only known aliases and an empty lock
before granting read access. It does not grant the authentication home or tmp tree.

```sh
export OPENLINKER_AGENT_NODE_ADAPTER=codex # or claude
export OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native
export OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE=true # CLAUDE_SESSION_REUSE for Claude
export OPENLINKER_AGENT_NODE_SESSION_ROOT=/absolute/private/node-sessions
# Keep your existing HOME and, if configured, CODEX_HOME / CLAUDE_CONFIG_DIR.
# No additional CODEX_API_KEY or ANTHROPIC_API_KEY is required for cached login.
```

`SESSION_ROOT` must be owner-only and have no unsafe writable ancestors. Do not
run Node as root. Options use the `OPENLINKER_AGENT_NODE_` prefix:

| Option | Meaning |
| --- | --- |
| `SESSION_ISOLATION` | `off` (default) or `native` |
| `SESSION_ROOT` | Private persistent storage |
| `SESSION_TEMP_ROOT` | Optional private, short temporary root (resolved path ≤40 bytes) |
| `SESSION_READ_PATHS` | JSON array of additional read-only code/library paths for shell tools; may not expose auth or session/control directories |
| `SESSION_NETWORK_DOMAINS` | JSON array of exact public HTTPS hostnames for sandboxed command networking; empty means offline commands |
| `CLAUDE_ALLOWED_TOOLS` | Optional subset of Read, Edit, Write, Glob, Grep, Bash |
| `CODEX_WEB_SEARCH` / `CLAUDE_WEB_SEARCH` | Controls the client's native search tool separately |

### Custom model gateways

Node no longer overwrites the default Codex model endpoint. Codex retains its
existing model-provider/auth configuration. Claude retains the configured
`ANTHROPIC_BASE_URL`/standard credential environment or cached login. Explicit
`CODEX_BASE_URL` and `CLAUDE_BASE_URL` overrides remain supported (HTTPS port 443,
complete API base path). An explicit Codex gateway uses `CODEX_API_KEY`, as before;
that optional path does not make a key mandatory for normal cached login.

`SESSION_NETWORK_DOMAINS` restricts **sandboxed command** networking, not the
trusted client's model API/auth traffic or its server-side WebSearch. Adding a
model gateway does not automatically grant tools access to it. TLS verification
is never disabled. Arbitrary environment forwarding remains rejected. Claude's
restricted mode ignores user/project customization settings; setups depending
on a settings-only helper or custom/cloud authentication need separate validation,
not automatic token extraction or a subscription proxy.

## Migration and verification

The old whole-client SRT design required dedicated keys and changed authentication
homes. The new host-auth scope is deliberately separate: old workspaces, native
history and potentially exposed credentials are neither copied nor deleted.
Resume starts fresh once when migrating, then persists in the new scope.
Remove `SESSION_SANDBOX_BIN`; Node rejects this obsolete override instead of
silently ignoring it. Legacy `SESSION_STORE`, delegation sockets and arbitrary
`ENV_ALLOWLIST` are not combined with native isolation.

Startup verifies CLI capabilities and OS prerequisites. On macOS Codex also runs
a real allowed-write/denied-read sandbox probe; Linux probes user/network/PID
namespace availability. Claude itself must initialize its
sandbox before a task, with `failIfUnavailable`; a help/version probe alone is
not evidence of file enforcement. Failures never select an unsandboxed fallback.

Run `scripts/test-native-session-isolation.sh` with both official client paths.
The acceptance suite uses private synthetic auth homes and local mock model
responses to exercise actual tools, cached auth, A-B-A recovery, credential-file
reads, symlinks, cross-session reads, scoped file edits, outside writes,
environment and parent-process probes. macOS additionally checks a synthetic
item in an explicitly named temporary keychain, never the login keychain.
These tests do not spend subscription/API quota and do not establish real model,
WebSearch or account-policy acceptance. The macOS/Linux CI matrix runs this suite.

Linux can create an identically named file in an empty private tmpfs overlay.
The acceptance check verifies that host credential files/directories remain
unchanged; successful writes to disposable overlays do not imply host access.

There are still **no per-session disk, memory or process-count hard quotas**.
`SESSION_TEMP_ROOT` and timeouts do not provide them. A task can exhaust its
filesystem or account budget without reading credentials. See the
[follow-up record](native-session-isolation-follow-ups.md).
