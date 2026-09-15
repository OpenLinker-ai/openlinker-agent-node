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
