# Codex app-server turn lifecycle

`codexturn.Run` owns one invocation of the pinned Codex app-server protocol:
initialize and delta opt-outs, thread start/resume, scoped turn events and final
messages, exact missing-rollout errors, interruption and bounded shutdown.
Node and Plugin call this same implementation. No SDK Worker, Node service,
Browser implementation or product configuration is imported by this package.

Callers supply `Config.Prepare`, which returns an **unstarted** `exec.Cmd`, the
workspace path visible to the client, and an optional cleanup function. Run
owns stdio and calls cleanup after the process is stopped and waited for, even
when preparation, startup or execution fails. The factory must configure
process-tree cancellation; its context remains live long enough to interrupt
the active turn before killing the process. Request cancellation is checked
before preparation and immediately before `Start`.

`PrepareNative` provides the shared POSIX native mechanism: canonical workspace
resolution, allowlisted environment, isolated CODEX_HOME or the trusted
launcher handoff, process-tree configuration and home cleanup. The caller's
argument builder receives the canonical workspace. Container integrations use
their own factory and container-visible workspace; host path resolution and
native home preparation do not run for that factory.

### macOS native cancellation and ownership

Native macOS turns additionally start an identity-bound descendant guard before
RPC initialization. It samples process metadata every 20 ms, follows only live,
verified ancestry from this invocation, and records at most 1,024 identities
(PID, effective UID, kernel birth time) that may still need cleanup. A record is
dropped only when the same snapshot shows its PID with another birth time, or an
exact lookup confirms that unlisted PID is gone or reused; alive-but-unlisted and
unreadable records stay and are still reported. Many short-lived tool commands
in one long turn therefore do not exhaust the bound. A recorded child/grandchild remains
owned after changing process group/session or being reparented. A reused PID
needs fresh verified ancestry; an old PID alone never adopts another session.
No process command names, environment, command arguments or user-wide kill are
used to select targets. Metadata access failure prevents turn startup; later
tracking/capacity/cleanup failures return `ErrProcessCleanup`.

Cancellation still sends the exact thread/turn `turn/interrupt`, bounded at
1.5 seconds, then closes stdin and gives the provider up to 500 ms to finish its
own tool teardown. An `interrupted` notification is not OS cleanup evidence.
The existing root process-group fallback then runs, followed by up to one
second of identity-revalidated cleanup of observed descendants, before `Wait`
and temporary-home removal. Existing `WaitDelay` remains two seconds for pipes.
Successful turns retain the existing two-second EOF drain. Callers must preserve
`ErrProcessCleanup` even when their request context is already canceled;
neither cancellation ACK nor this error changes Core's terminal authority.

This is **bounded cleanup, not hostile-process containment**. A double-fork that
reparents before its first observation can escape discovery. macOS lacks an
atomic identity-bound kill operation: the birth-time recheck immediately before
`kill` reduces PID-reuse risk, but is not a pidfd guarantee. The guard does not
extend file/network isolation, follow other users, or claim detached-descendant
coverage on Linux/Windows. Container factories retain their own cleanup
ownership unless explicitly opting into `TrackNativeDescendants`; ordinary
`PrepareNative` and Node's host-auth native command factory opt in. The latter
still runs on the host; its tool sandbox is not a container ownership boundary.

The Darwin shared fixture exercises each consumer's actual provider plus this
leaf with live child/grandchild processes in separate sessions/PGIDs: stubborn
tools after interrupt ACK, intermediate-parent reaping/reparenting, delayed EOF
cleanup, and provider exit first. It proves the targets live before cancellation
and exit before fixture finalizers, while a same-user/same-command independent
process keeps a heartbeat and another sentinel stays alive. Unit tests reject
PID/birth/UID changes with zero signals and cover the observed-identity bound,
including records retained or dropped by exact lookup. A real OS regression
runs 1,100 short-lived commands, then still reaps a lingering descendant.
Run these OS tests where macOS process-metadata access is allowed; a confined
test runner returning EPERM is not a passing cleanup test.

Product policy remains at the caller:

- launch arguments, model/approval/sandbox choices and credential domains;
- native session scope, reuse decisions, persistent maps and recovery retries;
- `BeforeThread`, after initialized and before thread start/resume (Plugin uses
  this to install its declared Browser package; errors stop execution);
- `Observer` for normalized, correctly scoped item progress, plus `Emit` for
  shared retry and semantic-failure status events;
- result metadata and diagnostics. Node's Claude ID hashes are unrelated to
  this Codex protocol leaf and do not change Plugin result fields.

Provider tests invoke each product's own production entry with shared
`providertest` fixtures. They cover delta opt-outs, malformed/oversized output,
scope filtering, missing-session recovery, cancellation before launch and
before turn/start reply, and draining output before Wait. Leaf tests additionally
cancel inside the command factory and verify that no process starts and that
factory resources are released. Plugin tests cover Browser install ordering,
installation errors and unexpected app authentication.

Publish an immutable Node module before updating Plugin's exact version and
checksums. Temporary workspaces only validate source composition; they do not
prove a new dependency is available from the public Go proxy/sumdb.

The exact observed diagnostic suffix `Incomplete response returned, reason:
max_messages` is a semantic stop. A correctly scoped error interrupts the turn,
discards any partial final answer and returns `ErrResponseMessageLimit`, even
without an event subscriber or when `willRetry` is false. An unresponsive client
still has bounded shutdown. This prevents repeated native retries of the known
message-limit response; other retry notifications retain `provider_retrying`
and the client's existing retry behavior.

The optional failure event contains only `provider=codex`,
`status=provider_failed`, `phase=failed`, `provider_error_kind=incomplete_response`
and `provider_error_reason=max_messages`. Raw errors/additional details can
contain credentials and are never exported. This does not identify which
gateway or upstream imposed the limit, infer a numerical limit, or make the
failed task successful. Keep Plugin's installed-client local Responses API
regression when upgrading Codex: a fabricated app-server notification alone
cannot detect changes to the client's diagnostic wording.

Node and Plugin retain the observed native thread on this specific failure
when session reuse is enabled and a trusted session key is present. An explicit
next Run can continue the existing context; this failure does not replay tools,
rotate Browser attachments or silently start a fresh native thread. A later
follow-up may still encounter the upstream limit. Other failure persistence
and missing-session recovery policies remain unchanged.
