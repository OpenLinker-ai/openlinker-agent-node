# Native isolation: open security and delivery work

Status: experimental, trusted-callers-only. This is a tracking record, not an
implemented credential/resource boundary or permission to expose a public Agent.

## P1: keep real provider credentials outside the sandbox

Current clients receive a dedicated provider key. Callers must be trusted to
receive it because final answers and streamed Run events are return channels.
Environment filtering and a network allowlist do not close those channels;
string redaction is not a solution to arbitrary encoding/chunking of a key.

Candidate design: a per-Run broker outside the sandbox authenticates approved
model API requests and supplies the real provider header upstream. The client
receives only a scoped placeholder or broker capability, never the real key.
SRT already runs its egress proxies outside the sandbox, but injecting a header
into ordinary HTTPS CONNECT traffic is insufficient: this requires a reviewed
provider endpoint adapter or TLS termination/trust design.

Before enabling untrusted callers:

- Verify Codex and Claude API compatibility, streaming/cancellation, proxy
  authentication and certificate verification without importing personal login.
- Bind the broker to the trusted session/Run and exact provider origins, methods
  and API paths; reject arbitrary forwarding, redirects, caller-supplied auth,
  cross-session capabilities and access to privileged provider-management APIs.
- Bound cost/request rates and revoke the per-Run capability at completion or
  cancellation. An exposed broker capability must not become a permanent key.
- Prove no real key is present in sandbox environment, argv, files, `/proc`,
  process inspection, crash output, generated configs or proxy/client logs.
- Exercise both final and streamed Run output using adversarial encoding and
  child processes. Test WebSearch/model calls separately using approved test
  credentials; protocol fixtures alone do not establish this property.

This PR does not implement that broker. Until these checks pass, retain the
trusted-caller restriction even if a particular OS blocks the parent-env probe.

## P2: enforce per-session resource limits

`SESSION_TEMP_ROOT` permits an administrator-selected quota-backed storage
location and fixes the client's actual TMPDIR. It does not add quotas.
Persistent and temporary storage can still exhaust their filesystem (or memory
when tmpfs-backed). A shared mount limit cannot prevent one session starving its
neighbors, and cancellation/timeouts are not a byte/inode limit.

Before claiming resource isolation, design and verify both macOS and Linux hard
limits for persistent bytes/inodes, temporary bytes, memory and orphan cleanup.
Quota/backend unavailability must fail closed for the selected resource policy.
Use small dedicated test volumes; do not fill the host's real root or `/tmp` to
test this. Do not silently change an enrolled Node's storage or mount policy.

## Runtime upgrade gate

The embedded runner intentionally depends on the pinned runtime's internal
module/command format. Upgrade its lock, top-level pin and runner adapter as one
reviewed change; install with `npm ci --ignore-scripts`, and run real macOS/Linux
enforcement, credential probes, both provider continuations and concurrency.
Unknown outer command syntax is a startup error, not an unsandboxed fallback.
The binary package checksum covers the bundled lock/installer; installed files
and the configured runtime remain trusted inputs, not attested executables.
