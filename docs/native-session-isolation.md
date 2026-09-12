# Native session isolation (macOS and Linux)

**Experimental; trusted callers only.** `ProviderConfig.SessionIsolation`, the
`sessionsandbox` package and `OPENLINKER_AGENT_NODE_SESSION_*` options do not
carry a stable compatibility promise. Upgrade the Node binary, bundled runtime
lock and adapter tests together.

Every caller allowed to use this Agent must be trusted to receive the provider's
model API key. A client or tool can copy a readable credential into the final
answer or streamed Run events; the network allowlist cannot stop that return
channel. Removing a key from a child's environment alone is not a credential
boundary against parent-process inspection. Do not expose this mode to public
or otherwise untrusted callers who must not know that key. Use a dedicated,
limited provider key and restrict callers through Core; this mode does not add
an authorization policy or enforce that trust judgment automatically.

This opt-in mode runs the **entire Codex or Claude Code client and its child
tools** under an OS sandbox. Docker is not required. It uses Anthropic's
[sandbox runtime](https://github.com/anthropics/sandbox-runtime), pinned to
`0.0.76`: Seatbelt on macOS and bubblewrap, PID/network namespaces and seccomp
on Linux. Missing dependencies or a failed enforcement probe stop startup;
there is no automatic unsandboxed fallback or weaker nested-sandbox mode.

This is a new source implementation, not a published Agent Node release or a
change to any running deployment. Plugin does not acquire this product policy
automatically by importing the shared packages. Plugin's Provider/Browser
container delivery and the separate Node Docker draft are not part of this
host-binary path. A deployed Provider container is not, by itself, evidence of
a separate container for every conversation.

## One Node, multiple session sandboxes

Start one long-lived `openlinker-agent-node` for the configured Agent/provider.
Its single SDK Runtime Worker dispatches Runs to the same adapter instance;
the adapter selects a sandbox from the trusted Core conversation scope.
No extra Agent Node process, registration or SDK DataDir is needed per chat.

```text
One Agent Node process (one SDK Runtime Worker, one configured provider)
  ├─ conversation A → sandbox A → client + child tools → A's workspace/history
  └─ conversation B → sandbox B → client + child tools → B's workspace/history
```

OS sandboxes apply to processes, not to Go goroutines or an ID inside a shared
client. Each executing Run therefore starts a separate sandboxed Codex/Claude
client process and the backend's helpers. The Node stays outside those
sandboxes as their trusted coordinator. A single client process is never used
to host mutually untrusted conversations with different filesystem rights.

Only session state is persistent: a later Run for A opens A's private storage
and resumes its native ID in a new client process. The sandbox invocation is
closed after the Run; an idle conversation does not retain a client process.
`OPENLINKER_AGENT_NODE_CAPACITY` controls Worker execution capacity; set it to
`2` or more to allow different sessions to overlap when Core dispatches them.
A busy session's exclusive lock rejects another simultaneous Run for that same
scope; Node does not add its own assignment queue or duplicate SDK scheduling.
Canceling one Run terminates that Run's ordinary process group, not the Node or
another session's client.

One Node currently selects one adapter/provider configuration. Multiple chats
on that provider share the Node; this mode does not introduce per-chat provider
selection or turn a Node into a combined Codex/Claude service.

## Installation and configuration

Install Node.js >= 20.11 and `rg`. Linux also requires `bwrap`, `socat` and
unprivileged user/PID/network namespaces; host security policy must permit the
installed bubblewrap executable. Do not disable a host-wide security policy or
enable SRT's weaker nesting mode to make the probe pass. Unsupported hosts fail
closed. macOS requires `/usr/bin/sandbox-exec`.

Ubuntu 24.04 can additionally deny bubblewrap's namespace setup through
AppArmor, including `loopback: Failed RTM_NEWADDR: Operation not permitted`.
An administrator must explicitly authorize the distro-owned `/usr/bin/bwrap`
executable in the host's AppArmor policy when user namespaces are restricted.
Use Ubuntu's documented [application-specific namespace authorization](https://discourse.ubuntu.com/t/ubuntu-24-04-lts-noble-numbat-release-notes/39890),
reviewing any existing site profile; do not disable the global restriction,
run the Node as root, or share the host network as a workaround. The CI-only
`scripts/ci-bwrap.apparmor` demonstrates this prerequisite on a disposable
runner. Node never installs that profile or changes host security settings.
This AppArmor authorization enables the trusted launcher; the actual per-session
filesystem/network boundary is still imposed by bubblewrap and seccomp.

From a source checkout, install the locked optional dependency:

```sh
npm ci --prefix tools/native-sandbox --ignore-scripts
export OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN="$PWD/tools/native-sandbox/node_modules/.bin/srt"
```

Binary archives built from this change also include `native-sandbox/` with the
same `package.json`, `package-lock.json` and `install.sh`. After verifying the
archive SHA-256, from the extracted archive directory explicitly run:

```sh
sh ./native-sandbox/install.sh
export OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN="$PWD/native-sandbox/node_modules/.bin/srt"
```

This uses `npm ci --ignore-scripts`, fixing all transitive versions and verifying
tarball integrity without lifecycle scripts. Older published archives may not
contain this bundle; do not mix their binaries with an unrelated release's lock.
These packaging changes do not themselves publish an artifact. Startup validates
the top-level package identity/version, not the integrity of a subsequently
modified installation or SRT's unrelated CLI `--version`. The configured package
is a trusted host input. Node never downloads a backend during a Run.

Example for Codex (retain your existing Node registration/connection settings):

```sh
export OPENLINKER_AGENT_NODE_ADAPTER=codex
export OPENLINKER_AGENT_NODE_SESSION_ISOLATION=native
export OPENLINKER_AGENT_NODE_SESSION_ROOT=/srv/openlinker/native-sessions
export OPENLINKER_AGENT_NODE_CODEX_SESSION_REUSE=true
export OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS='["api.openai.com"]'
# Provision CODEX_API_KEY through your service's dedicated secret environment.
```

Claude uses `OPENLINKER_AGENT_NODE_ADAPTER=claude`,
`OPENLINKER_AGENT_NODE_CLAUDE_SESSION_REUSE=true`, a domain list containing
`api.anthropic.com`, and a dedicated `ANTHROPIC_API_KEY` or private
`ANTHROPIC_API_KEY_FILE`. It runs with `--bare` and an empty strict MCP config.
Personal OAuth, Keychain login, plugins, hooks and session directories are not
imported. The client needs its model credential; this mode does not conceal that
credential from the client or from a trusted host administrator.

The root's parent must already exist. The root must be owned by the non-root
Node user, private (`0700`) and not a symlink. Shared writable ancestors without
sticky-bit protection are rejected. Existing state is never chmodded or moved.

| Setting | Behavior |
| --- | --- |
| `OPENLINKER_AGENT_NODE_SESSION_ISOLATION` | `off` (default) or `native`; unknown values rejected |
| `OPENLINKER_AGENT_NODE_SESSION_ROOT` | Absolute private persistent storage root |
| `OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT` | Optional private temporary-storage parent; default `/tmp`; resolved path at most 40 bytes |
| `OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN` | Pinned `srt` installation; defaults to PATH lookup |
| `OPENLINKER_AGENT_NODE_SESSION_READ_PATHS` | JSON array of additional read-only code/library paths; default empty |
| `OPENLINKER_AGENT_NODE_SESSION_NETWORK_DOMAINS` | JSON array of exact public DNS names, HTTPS port 443 only; default empty (offline) |

Only OS binaries/libraries, public system trust/config files, the selected client
executable and the current session's data/temp directories are readable by
default. A client installed through npm may also need its package directory and
Node executable in `SESSION_READ_PATHS`; explicitly grant those code paths, not
a personal HOME or all of `/opt`/`/usr/local`. The preflight runs the actual
installed client's version/help inside the same policy and rejects an
installation missing needed runtime paths. Read grants cannot include the
session root, another session, control state, HOME itself or their ancestors.
Additional read grants are shared inputs visible to every session using that
configuration; do not put secrets or other sessions' files there.

Network grants do not enable WebSearch by themselves. Keep the provider tool
switches/permissions configured separately. No host proxy settings, SSH agent,
platform token or arbitrary environment allowlist is inherited. The sandbox
proxy enforces exact domains/port and rejects loopback, link-local, host-local,
private and configured reserved address ranges; raw network/socket bypass is
blocked by the OS boundary. Browser and host MCP delegation sockets are not
supported in this mode and cannot be silently forwarded through it.

Critical loopback, unspecified, link-local/metadata, private, multicast and
selected cloud-platform addresses are explicitly denied in addition to the
pinned runtime's default resolved-address guard. Linux's outer command is
strictly decoded to argv and executed directly as bubblewrap; only the trusted
proxy/seccomp script inside the sandbox uses a shell. Unexpected generated
command syntax or missing required namespaces stops execution.

## Temporary storage and resource exhaustion

Neither the persistent root nor per-Run temporary data has a Node-enforced
byte/inode quota. By default each Run can write its private `/tmp/olns-*`
directory. A single caller can exhaust the underlying host filesystem; where
`/tmp` is tmpfs, this can also consume host memory. Private directories, cleanup
on ordinary exit and execution timeouts do not prevent exhaustion during a Run.

Set `OPENLINKER_AGENT_NODE_SESSION_TEMP_ROOT` to a dedicated quota-backed
filesystem directory provisioned by the administrator. Its parent must exist,
it must be owner-only and not a symlink, and the resolved path must be at most
40 bytes to leave room for Unix socket names. Node restores TMPDIR/TMP/TEMP at
client exec after SRT's wrapper so both clients actually use the private path.
Only the invocation subdirectory is removed on close; the configured parent
and persistent session state remain.

Moving storage alone is not a quota. Put both persistent and temporary roots
on storage with enforced limits, accounting for all concurrent sessions. A
shared volume limit protects the host's other storage but does not fairly
divide space between sessions. Hard per-session disk/inode/memory limits remain
an open requirement before admitting adversarial callers; Node does not create
mounts, set quotas or change host resource policy automatically.

## Persistence and protection

The persistent scope combines the stable Core URL, provider, trusted Agent ID,
Core principal scope and Core conversation key using length-framed hashing.
Caller payload/metadata, a model-provided path and legacy conversation fallback
IDs cannot select the scope. A different Run, Runtime Session or epoch does not
split the same conversation.

Each scope has its own workspace, HOME, CODEX_HOME and CLAUDE_CONFIG_DIR. Its
native-ID mapping, lock and generated policy live outside the readable client
tree. A cross-process lock spans execution and mapping updates. Other sessions'
histories/workspaces and ordinary host private files remain outside the read
allowlist, including through symlinks and child processes. macOS additionally
denies user preferences, Keychain Mach access and POSIX shared memory/semaphore
channels that SRT's general-purpose profile permits.

Enabling isolation creates a fresh protected native session, seeded from the
trusted Core history. It does not import the old session map or personal native
history. The normal configured workspace is replaced with this session's empty
persistent workspace; host projects are not automatically copied or mounted
writable. Legacy `SESSION_STORE` overrides and arbitrary `ENV_ALLOWLIST` entries
are rejected while native isolation is enabled. Defaults when isolation is off
remain unchanged.

Codex's inner sandbox is set to `danger-full-access` **only after** an outer
sandbox has been constructed. This avoids unsupported nesting; the whole
app-server and its tools remain behind the outer boundary. The result records
`session_isolation=native` separately from `codex_sandbox`.

## Scope of the assurance

The boundary protects session/host data against sandboxed client processes.
All untrusted sessions sharing this OS user must use isolation; a legacy
unsandboxed session is not constrained by another session's sandbox.
Trusted same-user host programs, Node, its configured binaries/runtime packages,
the OS/kernel and administrators remain outside that boundary. This is not a
per-session CPU, memory, persistent-disk or inode quota system. Process-group
cancellation covers ordinary client descendants; daemonized descendants and
crash recovery require separate lifecycle guarantees and must not be described
as container-equivalent cleanup.

Run the real boundary and continuation suite on each target OS:

```sh
export OPENLINKER_TEST_NATIVE_SANDBOX_BIN="$PWD/tools/native-sandbox/node_modules/.bin/srt"
bash scripts/test-native-session-isolation.sh
```

The provider continuation peers are deterministic Codex/Claude protocol clients
that perform real filesystem/process operations under the real OS sandbox.
They prove concurrent sessions through one Node's production Runtime handler,
same-session ownership rejection, cancellation without stopping another session,
and A→B→A persistence across Node-side process restarts. The restart helpers are
additional recovery tests, not a requirement to launch a Node for every chat.
These tests do not exercise Core transport/scheduling or prove authenticated
model or WebSearch success. Optional installed-client
probes use `OPENLINKER_TEST_NATIVE_CODEX_BIN`, `OPENLINKER_TEST_NATIVE_CLAUDE_BIN`
and JSON `OPENLINKER_TEST_NATIVE_READ_PATHS`, without sending a model request.
