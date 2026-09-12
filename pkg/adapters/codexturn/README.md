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
  the shared retry status event;
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
