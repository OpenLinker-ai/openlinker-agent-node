# Native isolation review follow-up — 2026-09-12

Base: Node main `4b50ed17bf4d7aae81f469c29acd1dc54fc3bd73` (PR #33).
Branch: `codex/native-isolation-hardening`. Source candidate only; no published
binary, root/Plugin pin update, real key use or running Agent cutover.

## Addressed review items

- P1: bilingual entry points now require callers trusted to receive the model
  key. Run final/streamed output is explicitly a credential return channel.
  A production-provider-path fixture starts a child without the key, reads the
  parent via `/proc` or `ps eww`, and returns only a digest through Run output.
  A missing executable/control is not accepted as evidence of denial.
- P2 storage: expose a private, short `SESSION_TEMP_ROOT`; validate owner,
  permissions, symlinks and socket path length, prohibit grants exposing its
  sibling invocation directories, and preserve the configured parent on cleanup.
  Restore TMPDIR/TMP/TEMP after SRT's override so client writes reach this path.
  This is storage placement, not a per-session byte/inode/memory quota.
- P2 dependency delivery: archive staging includes the exact source manifest,
  transitive lock, explicit script-free npm installer and bilingual guides.
  Packaging and install-command tests exercise the actual staging function;
  an actual local install retained the lock byte-for-byte and installed SRT
  0.0.76, socks5-server 1.0.10, commander 12.1.0, node-forge 1.4.0 and zod 3.25.76.
- P3: Linux directly executes validated bubblewrap argv; required network,
  user/PID namespaces and fresh `/proc` must be present. Explicit destination
  denials cover loopback/link-local/metadata and other reserved ranges. Public
  config/package/environment surfaces are marked experimental.

## Local evidence

macOS arm64 parent-credential probes passed for both provider peers. `/bin/ps`
works outside the sandbox but is denied at exec inside it (`EPERM`); the child
does not inherit the synthetic key and recovers no parent key. This records
that probe's rejection, not a proof that real client credentials are hidden.
The initial probe incorrectly required a sandboxed ps positive control that
needed the denied operation too; the corrected test verifies the same binary
outside, existence inside and the precise execution permission error.

The new temporary-write test initially revealed SRT resetting TMPDIR to its
shared default. Restoring the per-Run directory at client exec fixed the test
without widening filesystem grants. The configured-temp and explicit-address
tests pass locally. The installer test also normalizes macOS's `/var` symlink
when comparing its resolved installation path.

The complete local `GOWORK=off go test -race -count=1 -v ./...` passed with
`OPENLINKER_TEST_NATIVE_SANDBOX_BIN` pointing to a fresh installation from the
staged binary bundle. `go vet ./...`, `go mod verify`, generated RPC checks and
the isolated adapter boundary checks passed. All six Node script tests passed,
including the negative dependency graphs, generated-command parsing and actual
bundle staging/installer invocation. The unchanged Plugin consumer passed
`go test ./internal/pluginhost/...` in a temporary Go workspace using this Node
candidate. Full-tree secret scanning reported only three previously reviewed
fixture values in unchanged tests; no new finding was introduced.

## Real OS CI evidence

All three jobs (Linux/macOS native sandbox and full test/build checks) passed
for implementation commit `e6672948` in
[CI run 34701952222](https://github.com/OpenLinker-ai/openlinker-agent-node/actions/runs/34701952222).

| System | Codex adapter + deterministic peer | Claude adapter + deterministic peer |
| --- | --- | --- |
| Linux / bubblewrap | **Known exposure:** child with no key read parent environ; matching digest reached Run output | **Known exposure:** same result |
| macOS / Seatbelt | `ps eww` denied at exec; no parent key recovered | Same result |

Linux logs explicitly mark `KNOWN EXPOSURE` in both credential subtests. Their
passing result means the probe ran, verified a key-free child and classified
the observed behavior; it does **not** mean credentials were protected. The
macOS result matches the local probe and does not rule out other return paths.
These tests use synthetic keys and deterministic protocol peers through the
actual production adapters, never authenticated Codex/Claude model calls.

Configured temporary storage writes, generated-command parsing, actual OS
filesystem/network/process checks and both provider continuation paths passed
on both systems. P1 remains open for untrusted callers, as do hard resource
quotas. This source candidate is still a draft PR, with no binary release or
running deployment change.

Remaining broker/resource work is tracked in
[native-session-isolation-follow-ups.md](native-session-isolation-follow-ups.md).
