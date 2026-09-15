# Changelog

All notable changes to OpenLinker Agent Node will be documented in this file.

This project is currently pre-1.0. Breaking changes may happen before the
runtime protocol, adapter interfaces, and CLI behavior are declared stable.

## Unreleased

- Fix ordinary native Codex cancellation on macOS leaving observed tool
  children/grandchildren alive after they changed process groups. Keep scoped
  `turn/interrupt`, allow bounded EOF teardown, then revalidate recorded kernel
  process identities before cleanup, including after parent exit. Preserve
  cleanup errors even when the Run context is canceled. Real OS regressions
  keep independent same-command and unrelated processes alive. This is bounded
  observed-ancestry cleanup, not hostile double-fork containment or a new
  file/network sandbox; other OS/process factories keep their existing scope.

- Classify the observed Codex `max_messages` incomplete-response diagnostic in
  scoped retry status events shared with Plugin. Export only fixed kind/reason
  values, never raw provider errors, credentials, endpoints or request text.
  Unknown diagnostics and native retry/recovery behavior remain unchanged.

- Add a read-only `--check-config` command and the same allowlisted native policy
  summary before startup preflight. Report search, native isolation, session reuse
  and host-auth concurrency without exposing credentials, paths or endpoints.
  Warn when native isolation is off or serial admission has capacity above one.
  Configuration checking never starts a Worker/provider or proves enforcement;
  existing opt-in defaults and provider/OS preflight behavior remain unchanged.
  Reject explicit native-only path/network JSON settings even when they decode
  to empty arrays or null while isolation is off, instead of silently ignoring
  the requested policy. Ordinary mode should omit those settings.

- Wire `OPENLINKER_AGENT_NODE_CODEX_WEB_SEARCH` from the Node environment entry
  through CodexAdapter to the launched provider. Default false still disables
  native search; true enables live search for new and resumed conversations.
  Invalid values fail before startup. Approvals, host authentication, native
  isolation, session paths, and other tool policies are unchanged. Releases
  through `v0.1.58-rc.3` ignored this setting; follow fresh-enrollment rules when
  adopting a new binary version.

- Pin the verified Go SDK module at `63fc87d73406`, adopting the gRPC
  1.83.2 and protobuf dependency updates while retaining the SDK Go 1.25 baseline.
  Command, credential, session and persistent-state contracts are unchanged.

- Pin the shared SDK contract synchronization commit `6da420c00979`. This
  updates module identity and contract metadata; the Go SDK production sources
  and existing command, credential, session, and persistent-state behavior are unchanged.

- Move native admission locks from predictable `/tmp` files to private state at
  `$HOME/.local/state/openlinker-agent-node/host-auth-<provider>.lock`. Services
  with PrivateTmp coordinate when they share the underlying HOME storage. Separate
  HOME mounts/values do not. Reject unsafe parents/symlinks and reserve this path
  from tools. Stop older `/tmp`-lock Nodes/clients before upgrade; no session reset.
  Recommend capacity 1 for serial mode; polling remains non-FIFO and timeout-bound.

- Native clients now default to cooperative serial admission per shared host HOME/provider,
  across Node processes and session roots that share the same HOME storage. Waiting respects Run
  cancellation and timeout; `HOST_AUTH_CONCURRENCY=client-managed` explicitly
  allows parallel clients. Existing native capacity greater than one will queue
  provider work by default; session scope/history is unchanged. This does not
  coordinate external clients or guarantee OAuth rotation/orphan-process safety.
- Expose `appfiles.ErrLockBusy` for typed contention without changing the legacy
  contention message; unsafe lock files fail instead of being retried as busy.
- Native isolation now reuses the installed Codex/Claude client's authentication
  and restricts its tools. No per-session login or mandatory dedicated key.
- Native Claude local tools now default to Bash only. In-process Read/Grep/Glob/
  Edit/Write require explicit `CLAUDE_ALLOWED_TOOLS` opt-in for trusted workloads;
  file tools are not covered by the Bash OS sandbox. Add real-client link probes
  and distinguish host-preseeded hard links from sandbox-created links.
- Explicitly disable Codex `view_image` and host `notify` in native mode. Put
  Claude command bookkeeping under the Run's private temp directory as well.
- Deprecate historical `sessionsandbox.Open`/`Session.Command` without deleting
  their experimental compatibility API; track coordinated runner/CI removal.
- Migration from whole-client SRT to host-auth mode starts a fresh workspace and
  native session once; old directories/private histories are retained, not copied
  or deleted. Subsequent host-auth Runs reuse the new scope. The later Bash-only
  default/view-image hardening does not reset it again. Core history is separate.
  Remove the obsolete SESSION_SANDBOX_BIN override. Model gateways no longer
  require a tool-network grant. Experimental: no resource-quota guarantee.
- Current host-auth source passed macOS and Ubuntu PR CI, plus isolated Ubuntu
  arm64 client acceptance. Real shared-login OAuth rotation remains unverified;
  synthetic-auth tests and Node admission do not prove account-wide protection.


### Added

- Keep native session isolation and its public/configuration surfaces
  **experimental, trusted-callers-only**. Add real-client cached-auth, scoped
  file-edit, cross-session, parent-environment and temporary-keychain probes
  using synthetic credentials; network filtering is not credential isolation.
- Historical SRT runtime remains a separately locked experimental leaf fixture;
  Node binary archives now carry host-auth setup documentation, without bundling
  an obsolete SRT installation.
- Optional `SESSION_TEMP_ROOT` selects private storage on an administrator's
  quota-backed filesystem. No byte/inode/memory quota is enforced by Node.
- Opt-in native session isolation for macOS and Linux using the installed
  client's tool sandbox. One Agent Node manages concurrent conversations;
  each Run launches a trusted client that reuses host authentication, with
  persistent scoped workspace/history, exclusive ownership and independent
  cancellation. The default remains off. Personal conversation history,
  Browser and host delegation are not imported.
  See `docs/native-session-isolation.md` for configuration and limitations.
- Share the complete Codex app-server turn lifecycle in `pkg/adapters/codexturn`.
  Native command preparation, protocol ordering, scoped events, cancellation
  and shutdown now have one implementation with command-factory, pre-thread
  and progress hooks. Product launch/session/tool policy stays in each caller.

### Fixed

- Wire explicit Codex and Claude model base URLs through the production Node
  adapters. Preserve nested API paths and reject ambiguous or credential-bearing
  overrides. Trusted-client model traffic is separate from tool-network grants.

- In the retained historical SRT leaf, decode the Linux outer sandbox command to argv and execute bubblewrap directly;
  reject unexpected command formats or missing required namespaces. Restore the
  private client TMPDIR/TMP/TEMP after SRT's override, and explicitly deny
  loopback, link-local/metadata and other reserved address ranges.
- Node now owns its agent-host v1 delegation commands and uses its own
  executable by default. No runtime dependency on the platform CLI or Plugin
  host is needed; Browser is not advertised. Tests build the real Node binary,
  exercise both provider transports, and verify default self-host startup.
- Claude delegation fails before provider probes and Worker startup when
  `--bare` lacks an API key. Direct and private-file sources share one resolved
  configuration for preflight and execution; errors redact values and paths.
  Ordinary Claude native authentication and Codex defaults are preserved.
- Wire `OPENLINKER_AGENT_NODE_CLAUDE_WEB_SEARCH` through the native Claude
  environment entry. Default false preserves the WebSearch/WebFetch deny;
  explicit true removes only that deny, without widening other permissions.
  Invalid values fail before startup. The rc.1 binary lacked this mapping;
  follow fresh-enrollment rules when changing the registered binary version.

### Local Claude bridge candidate — test prereleases only

- Build-injected Node identity (`dev`, exact release tag, or commit) and a
  read-only `--version` command that bypasses all runtime startup. Unsupported
  command arguments now fail instead of being ignored. This changes the
  enrollment version from the old hardcoded `openlinker-agent-node/0.1.43`.
  **Existing enrollment cannot be upgraded in place.** Test deployments without
  real users use a new NodeID, new unbound credential and new SDK DataDir after
  the old process has stopped with settled Attempts and empty spool; retain the
  old state. Ordinary active-Node restart keeps the same exact version and
  DataDir. No automatic rollback or general migration controller is supplied.
- The release gate now permits only canonical `v0.x.y-alpha.N`, `v0.x.y-beta.N`
  and `v0.x.y-rc.N` test tags, without an environment bypass. Stable/v1+ tags,
  malformed input and missing arguments fail. GitHub releases are marked
  prerelease; non-tag SHA artifacts remain CI-only. This replaces the blanket
  Core-upgrade prerequisite for fresh test enrollment, not Core's identity
  validation. WebSocket/pull intake and restart still need real verification.
- Long-lived Claude success diagnostics `claude_resume_session_id_sha256` and
  `claude_session_id_sha256`, bound to actual invocation/result IDs, with no raw
  IDs and no failed-retry evidence carried into a successful fresh invocation.
- Native Codex/Claude adapters and reusable protocol leaves belong to this
  repository, with no CLI/Plugin module dependency. Session reuse remains
  false by default; deep Plugin/Browser policy is unchanged.

### Breaking

- Removed Agent Node's duplicate Runtime client, transport supervisor,
  assignment journal, encrypted spool, and delivery state machine. There is no
  compatibility layer because the project is still pre-1.0.
- The pinned `openlinker-go` SDK now owns the complete reliable Runtime Worker.
  Public Go names are generation-free `Runtime*`, and Runtime URLs do not carry
  a protocol generation.
- Normal startup uses `OPENLINKER_URL` to discover the Runtime origin and its
  token-only or mTLS policy from `/.well-known/openlinker.json`.
  `OPENLINKER_RUNTIME_URL` remains an advanced address override but no longer
  bypasses discovery or downgrades its security policy.
- Delegated Agent calls require an explicit idempotency key. Reuse a key only
  for retries of the same intent.
- The optional public A2A listener no longer executes an Adapter or owns
  in-memory task and push-notification state. It retains only local Agent Card,
  bearer-authentication, request-size, and timeout guards; the Go SDK proxies
  every stateful A2A operation to Core over Agent Token plus the discovered security policy. REST and
  JSON-RPC Agent Card responses remain stateless and local so their external
  URL continues to identify the AgentNode listener.

### SDK boundary

- Token-only startup no longer requires `OPENLINKER_NODE_ID`; the pinned SDK
  derives a deterministic token-scoped identity. Explicit mTLS startup keeps
  the provisioned Node ID requirement.
- Pinned `openlinker-go` version `v0.2.0-rc8.0.20260908135527-31afbf9c1a18`;
  dependencies use standard Go module resolution instead of a checked-in vendor tree.
- The SDK owns discovery, token-only/TLS 1.3 mTLS policy, Session identity, WebSocket/Pull
  switching, assignment confirmation, lease renewal, resume, cancellation,
  drain, durable assignment state, encrypted Event/Result delivery, ACK repair,
  backpressure, and duplicate-execution prevention.
- Agent Node now owns only CLI and environment parsing, Adapter selection,
  HTTP/command/Codex/A2A execution, localhost helper sessions, process-tree
  control, the public A2A listener shell, and SDK file-store directory
  selection. Core owns public A2A message, task, run, stream, and push state.
- Prior builds identify themselves to Core as `openlinker-agent-node/0.1.43`;
  the version-injection candidate above is limited to fresh test enrollments. Direct SDK
  workers default to `openlinker-go/runtime-worker`.

### Verification

- Migrated Runtime failure-matrix and durable-store conformance coverage into
  the Go SDK, including crash/restart, ACK loss, cancellation, lease fencing,
  corruption, backpressure, and WebSocket/Pull recovery.
- Added static Agent Node boundary tests that reject reintroduced Runtime
  state-machine files, local public A2A authority, and direct Runtime wire
  operations, and require `NewRuntimeWorker` plus `NewRuntimeA2AProxy`
  integration.
- Added regressions for Stop during Worker startup and for Adapter error,
  cancellation, and panic mapping.
- Added token-only and mTLS proxy regressions for public A2A operation families, SSE,
  identity/header isolation, oversized requests, unavailable Core, and
  listener generation-safe shutdown/restart.
- Verified both repositories with ordinary and race-enabled Go tests and a
  read-only standard module dependency graph.
