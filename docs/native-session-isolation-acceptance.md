# Native session isolation acceptance — 2026-09-12

Source base: Agent Node `9054ba8d2d1ea3675a6a6e91c21808f9081bfbd7`.
Candidate branch: `codex/native-session-isolation`. This record covers local
source validation, not a merged/published module, binary, deployment or live
Agent cutover. Existing Docker PR #31 was not changed.

## Environments and results

| Check | Result |
| --- | --- |
| macOS arm64 / Go 1.27.1: full Node `go test -race ./...` with real sandbox tests enabled | Passed |
| macOS final affected provider/preflight and sandbox race checks | Passed |
| Linux arm64 / Ubuntu 26.04 / Go 1.26.4: native sandbox, provider and Node internal race suite | Passed |
| Linux final sandbox race check after runtime-library policy adjustment | Passed |
| Real macOS Codex 0.153.0 and Claude Code 2.1.259, sandboxed version/help probes | Both passed |
| Real Linux Codex 0.153.0 and Claude Code 2.1.259, sandboxed version/help probes | Both passed |
| Generated command quoting and macOS profile hardening tests | 2/2 passed |
| Release build-identity tests | 6/6 passed |
| Isolated module/package graphs, negative boundary controls, RPC generation check, `go mod verify`, `go vet`, whitespace check | Passed |
| Plugin host consumer tests using a temporary workspace with this Node candidate and the pinned SDK | Passed |

The Linux environment was a dedicated isolated VM (no host file sharing or SSH
agent forwarding), not a Docker container. Its kernel was
`7.0.14-orbstack-00380-ga7e0a2dc9535`, with bubblewrap `0.11.1`, Node `22.22.1`
and sandbox-runtime `0.0.76`. No weaker nesting/network setting was enabled.
CI now contains a macOS/Linux real-enforcement job; this local record does not
claim that the new GitHub Actions job has already run.

## What was exercised

- A→B→A native-ID and file-history persistence, with a separate Node-side OS
  process per turn and a new Runtime Session/epoch on A's continuation.
- Core namespace, provider, Agent, principal and conversation scope separation.
  Forged input/metadata, missing authority and legacy fallback IDs cannot select
  a session or reach its persistent storage.
- Direct reads, child-process reads, symlink and hardlink attempts against host
  canaries, the other session's workspace/history, native-ID mappings and policy.
  Host/control files were compared before and after attempted writes.
- Exact-scope concurrent ownership rejection and private/symlink state checks.
- Allowed writes/reads within the current session and temporary directory.
- Local TCP endpoint access denied both through the configured proxy and with
  explicit proxy bypass. The curl executable was positively checked first.
- Command argument quoting, missing/wrong runtime rejection, cancellation before
  launch, and ordinary descendant termination after cancellation.
- Fixed wrapper environment, removal of host proxy/loader/platform variables,
  and dedicated credential requirements on both production adapters.
- Installed official client compatibility probes run inside the same sandbox;
  Claude probes and Runs use bare mode, without personal login import.

Provider continuation peers implement the real Codex app-server/Claude stream
protocol boundaries and perform real OS probes, but their answers are
deterministic. Only synthetic credentials/canaries were used in those tests.
Installed official client checks made no model request.

## Findings resolved during verification

- macOS's short Unix-socket path limit required a short per-invocation private
  temporary directory rather than a nested persistent path.
- A second nested Seatbelt invocation is rejected by macOS. Additional IPC
  denials are appended to SRT's single generated profile before its launch;
  the decoder rejects unexpected generated shell syntax instead of evaluating
  it. Symlink metadata is allowed for runtime path traversal, not target content.
- Public ICU data and Debian/Ubuntu's externalized Node built-ins are necessary
  runtime inputs. The policy grants those specific system library locations,
  not personal HOME or all host configuration directories.
- Linux's final seccomp init must itself be readable inside bubblewrap; its
  exact executable from the pinned package is granted and checked.
- Linux can create a harmless private placeholder at an otherwise hidden host
  pathname. Tests check that original contents cannot be read and that the
  actual host/control file remains unchanged, rather than treating writes into
  the private namespace as writes to the host.
- The Linux source-copy harness initially omitted example fixtures and retained
  macOS archive metadata. A complete source-only archive corrected the harness;
  production assertions were not removed to accommodate those failures.

## Not established by this acceptance

Authenticated model, WebSearch and arbitrary tool end-to-end success; resource
quotas; protection from trusted host programs/admins/kernel; container-equivalent
daemonized descendant/crash cleanup; Windows support; Browser or host delegation
socket support. All untrusted sessions under the same host user must use the
sandbox; legacy unsandboxed sessions are outside its protection.

No Node registration, token, running process, SDK spool or pre-existing personal
session was replaced. Defaults remain off. See the
[configuration and boundaries](native-session-isolation.md) and
[Chinese guide](native-session-isolation.zh-CN.md).
