# Host authentication and tool isolation acceptance

Candidate: `codex/native-auth-reuse`, based on Node `be13e3a`. Initial admission implementation
`25c49b2c24449754d428a48bedf15fdd5be1d6bf`, verified on 2026-09-14 in
[PR #36](https://github.com/OpenLinker-ai/openlinker-agent-node/pull/36). This record
does not claim a release, root version update or deployment.
Only Node source changes. Plugin consumes the shared leaf in compatibility tests;
the root repository and Plugin pins are unchanged.

## HOME-lock follow-up

The follow-up in the same PR moves the coordination lock into private host HOME
state. Multiple participating Nodes must share the underlying state inode; different
HOME mounts/values are not coordinated. Native serial setups should use capacity
1; 50ms polling is non-FIFO and bounded by Run timeout. Stop older `/tmp`-lock
Nodes and their clients before upgrade; session scope/history does not change.

Local macOS and isolated Ubuntu arm64 tests exercise the production lock with
HOME aliases, unsafe directories, cancellation and process death. The Linux
`TestHostAuthPermitWithPrivateTmp` uses a real bwrap mount/user namespace with
empty `/tmp` and rebinds the same HOME at a different path. A sentinel confirms
that `/tmp` is private; the child waits until the parent releases the HOME lock.
It is a namespace regression, not a test of a shipped systemd unit (none exists).
Both actual client Run paths additionally try reading a synthetic state canary,
writing this directory and unlinking the lock. Host inode/content checks and
captured tool outputs confirm denial in the tested cases. Credentials remain
synthetic. The CI matrix executes this regression in the Linux native job;
the earlier CI links below document the pre-HOME-lock implementation, not the
follow-up's exact commit. Its final commit/checks are recorded in PR #36.

## Tested boundary

- Official Codex 0.153.0 and Claude Code 2.1.259, locked npm packages installed
  with `npm ci --ignore-scripts`. Current tests use macOS arm64, GitHub Ubuntu
  24.04 amd64 and isolated Ubuntu 24.04.5 arm64 (OrbStack kernel 7.0.14).
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
- Current GitHub Ubuntu testing uses the workflow's per-executable bubblewrap
  userns authorization if the global restriction is enabled, checks that the
  global value remains unchanged, and runs the actual host-auth/client suites.
  It does not use a privileged Docker container or disable global AppArmor.
- The separate Ubuntu arm64 machine has no host mounts or forwarded SSH agent.
  Its OrbStack kernel lacks AppArmor and the Ubuntu userns sysctl; no system
  policy was relaxed. Node 22.23.2 was downloaded from the official distribution
  with SHA-256 verification; bubblewrap is 0.9.0. This is additional Linux
  evidence, not a stock Ubuntu kernel test. An earlier offline Debian container
  relaxed its outer policies and is now only historical evidence.
- Admission tests drive both actual provider Run entrypoints, with two independent
  session roots and a blocked local model request. The second client cannot reach
  the model; canceling it leaves the first running; the next client proceeds after
  completion. Separate compiled-test subprocesses verify cross-process exclusion,
  cancellable waits, crash release, timeout/event errors and unsafe-path rejection.
  Crash release proves the lock primitive only, not orphan-client cleanup.
- Claude defaults to Bash. The opt-in matrix executes all five file tools, file
  and directory symlinks, sandbox-created and host-preseeded hard links, and Linux
  `/proc/self` aliases. Positive controls ensure tools actually work. Host-preseeded
  hard links disclose synthetic contents through Read/Grep and Bash: they remain
  explicitly unsafe inputs. Passing means the documented assertions hold, not
  that every attempted read is denied. Check/open link swaps remain untested.
- Codex host settings deliberately enable `view_image` and a synthetic `notify`
  command in the fixture; native mode still hides that tool and never runs notify.

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

[GitHub CI 34810002330](https://github.com/OpenLinker-ai/openlinker-agent-node/actions/runs/34810002330)
is successful for implementation `25c49b2`:

| Job/environment | Result |
| --- | --- |
| `test`, Ubuntu 24.04 amd64 | Full tests, race, vet, release build/staging and dependency boundaries passed |
| `native-session-isolation (ubuntu-latest)` | Actual host-auth, admission and file-tool/link tests passed, not skipped |
| `native-session-isolation (macos-latest)` | Same current client tests passed, including temporary synthetic keychain |
| Local macOS arm64 | Full `GOWORK=off go test -race -count=1 ./...`, real-client suite, workspace tests and vet passed |
| Isolated Ubuntu 24.04.5 arm64 | Compiled current adapter tests: host-auth, admission and complete link matrix passed; no race instrumentation in this cross-compiled binary |

The cached-auth suite uses 5 Codex and 13 Claude fixture requests per system;
the admission suite adds independent synthetic model calls. No real model or
subscription is used. The CI script also retains separate SRT regression coverage;
that coverage is not the proof for the current host-auth production path.

Six-target module/package boundary checks pass, including CGO variants. Plugin's
actual `packages/agent-adapters/agent` appfiles consumers pass both candidate
workspace and `GOWORK=off` temporary-local-replacement tests; its host packages
also pass with the local replacement. No dependency declarations were changed.
This is source compatibility evidence, not a Plugin version bump or root pin.

## Remaining limits

No per-session memory, disk, process-count or spending hard quota. Actual model
calls, WebSearch, live account refresh and all keychain/IPC attack paths have not
been accepted. Trust the official binary, OS administrator and approved code
paths; keep experimental access limited to trusted callers. This is not account
sharing authorization or proof against a compromised client. Native serial
admission cannot coordinate external/older/opt-out clients. A hard-killed Node
can leave a client alive after the lock releases; inspect and stop that Node's
remaining clients before restarting. No real token-rotation test or guarantee
of account-wide exclusion is claimed.

Old SRT histories stay untouched and are not imported. Migration starts a new
scope once; later Runs resume that scope. See [setup and migration](native-session-isolation.md).

Relevant primary references: [Claude sandbox configuration](https://code.claude.com/docs/en/sandboxing),
[Claude environment controls](https://code.claude.com/docs/en/env-vars),
[Codex argv0 helpers](https://github.com/openai/codex/blob/rust-v0.153.0/codex-rs/arg0/src/lib.rs).
