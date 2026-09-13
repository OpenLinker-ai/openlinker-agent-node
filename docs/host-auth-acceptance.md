# Host authentication and tool isolation acceptance

Candidate: `codex/native-auth-reuse`, based on Node `be13e3a`. Local validation
on 2026-09-14; this record does not claim a published module, release or deployment.
Only Node source changes. Plugin consumes the shared leaf in compatibility tests;
the root repository and Plugin pins are unchanged.

## Tested boundary

- Official Codex 0.153.0 and Claude Code 2.1.259, macOS arm64 and Debian 12 arm64.
- Each client uses a private synthetic authentication home. The initial Run has
  no API-key environment variable; cached authentication reaches a local model
  fixture. A later Run also checks optional credential environment scrubbing.
- Actual model-requested shell tools write the current workspace, attempt to
  read credentials, another conversation, an outside file and a symlink escape,
  inspect parent processes, and attempt offline-network escape. Tests inspect
  returned tool output and host files, not just launch parameters.
- A → B → A resumes exactly A. Claude additionally exercises Read, Write and
  Edit, with a successful in-workspace edit and a rejected outside write.
- macOS uses an explicitly named temporary keychain and synthetic password:
  readable by the outside positive control, not returned by either tool. The
  operator's login keychain is never queried or unlocked.
- Linux is exercised in a disposable non-root container with no host mounts and
  no external network. Container proc masking/seccomp/AppArmor are disabled only
  for this test harness so nested user/PID sandboxes can start. Writable fixture
  files live in tmpfs. Docker is not a Node runtime feature or requirement.

## Failures found and resolved during this work

1. Claude's global `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB` switch adds broad Linux
   writable roots. Those re-bind paths reopened denied reads in this combination.
   The final Linux path uses ordinary Bash sandbox credential-deny rules and its
   PID/proc isolation. Both cached-auth and environment-key probes pass.
2. Repeated `/bin`/`/usr` aliases in Linux root-deny overlays hid the shell.
   Canonical, deduplicated root masks preserve only the requested code grants.
3. Codex's argv0 helper aliases reside under its auth home's tmp directory.
   The thread override grants validated code-only alias directories and executable
   files, preserving base permissions; no auth-home or whole tmp grant is added.
4. Linux can create a matching pathname in an empty private overlay. The write
   oracle checks the real host directory rather than equating overlay writes with
   a host mutation. Credential contents still must never reach model input/output.

## Results

- Real-client acceptance: both providers pass on both systems (5 Codex and
  13 Claude fixture requests per system). No real model or subscription is used.
- Node: full `GOWORK=off go test -race -p 1 -count=1 ./...`, full candidate
  workspace tests, `go vet`, build, and 12 JavaScript tests pass.
- Dependency/package boundary checks pass for six OS/architecture targets,
  including CGO variants. No SDK, CLI or Plugin reverse dependency is introduced.
- Plugin's `packages/agent-adapters/...` passes both candidate-workspace and
  `GOWORK=off` local-replacement tests. This is source compatibility evidence,
  not public-module resolution or a Plugin version bump.
- CI now installs exact official-client versions from a lockfile with lifecycle
  scripts disabled. Linux verified the locator against those installed packages.
  The updated GitHub workflow itself has not run for this local candidate.

## Remaining limits

No per-session memory, disk, process-count or spending hard quota. Actual model
calls, WebSearch, live account refresh and all keychain/IPC attack paths have not
been accepted. Trust the official binary, OS administrator and approved code
paths; keep experimental access limited to trusted callers. This is not account
sharing authorization or proof against a compromised client.

Old SRT histories stay untouched and are not imported. Migration starts a new
scope once; later Runs resume that scope. See [setup and migration](native-session-isolation.md).

Relevant primary references: [Claude sandbox configuration](https://code.claude.com/docs/en/sandboxing),
[Claude environment controls](https://code.claude.com/docs/en/env-vars),
[Codex argv0 helpers](https://github.com/openai/codex/blob/rust-v0.153.0/codex-rs/arg0/src/lib.rs).
