# Native isolation follow-up status — host-auth candidate

The new Node candidate uses the installed official client's own authentication
and tool sandbox, not the historical whole-client SRT/key-broker proposal below.
Node does not intermediate subscription credentials. Separate file/command tool probes,
A-B-A and synthetic cached authentication are covered by the official-client
acceptance suite. This is candidate source work, not a binary release, deployment
or proof for untrusted public callers. Resource quotas and live account/model
acceptance remain open. The old SRT package is retained only for its experimental
leaf API and historical regression; Node native mode no longer uses that runner.

## Current candidate: file tools and legacy API

- Native Claude defaults to Bash. Read/Grep/Glob/Edit/Write require explicit
  operator opt-in. OS shell probes do not establish their application-level
  path checks. The installed-client link matrix tests both modes, including
  sandbox-created links and separately host-preseeded hard links. The latter
  disclose synthetic contents through Read/Grep and Bash and are not safe inputs.
  Defaulting to Bash does not repair an inode already exposed by the operator.
- Codex `view_image` and host `notify` are explicitly disabled. Claude's own
  command temp files also use the private Run temp directory.
- Deprecate `sessionsandbox.Open`/`Session.Command` now without removing the
  public signatures. Before a subsequent pre-1.0 removal: inventory released
  Node/Plugin consumers, announce the breaking change, then remove the legacy
  runner, `tools/native-sandbox`, staging script and their dedicated CI tests
  together. Keep `OpenClient`, storage/locking and official-client OS tests.
  Current candidate call sites are clean; root's still-pinned older Node source
  is not evidence that the migration has been released or deployed.
- Do not claim full credential isolation or untrusted multi-user readiness.
  Link-swap races, the complete client IPC surface, resource quotas and real
  account/model acceptance remain outside this test evidence.

The following record describes the old implementation and investigation. Its
parent-key exposure result is historical evidence, not a result for the new
host-auth tool boundary. Its proposed broker is not the current Node plan.

---

# Native isolation: open security and delivery work

Status: experimental, trusted-callers-only. This is a tracking record, not an
implemented credential/resource boundary or permission to expose a public Agent.

## P1: keep real provider credentials outside the sandbox

Current clients receive a dedicated provider key. Callers must be trusted to
receive it because final answers and streamed Run events are return channels.
Environment filtering and a network allowlist do not close those channels;
string redaction is not a solution to arbitrary encoding/chunking of a key.

The real Linux sandbox acceptance on 2026-09-12 reproduced parent-environment
key access in both provider-adapter paths with deterministic client peers: the
key-free child returned the matching synthetic-key digest through Run output.
macOS rejected ps at exec, which is only evidence about that one probe.

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

### 2026-09-13 design verification

The installed, locked SRT 0.0.76 exposes a TLS-terminating proxy with separate
`filterRequest`, `mutateHeaders` and `getBodySubstitutions` hooks. Its built-in
`credentials.envVars[].mode=mask` is **not** sufficient for model-key secrecy:
`sandbox-manager.js` wires both generic header and request-body substitution.
A local synthetic-only probe registered a fake key in `SentinelRegistry`, put
the resulting sentinel in a JSON `input` prompt, and passed it through
`createBodySubstitutionTransform`. The forwarded prompt contained the fake key
even though the original did not. This establishes a rewriting hazard, not a
real model disclosure or a flaw in the currently deployed Node (which does not
enable that masking feature).

Keep the broker separate from the custom-gateway wiring change. The proposed
implementation must:

- Reuse the pinned TLS proxy's HTTP parsing and verified upstream TLS, but
  provide a provider-specific authentication-header injector only. Never
  register a generic secret substitution for prompts, bodies or other headers.
- Pass the real key to the outside runner through a bounded, private descriptor,
  close it before launching sandbox helpers, and pass only a random per-Run
  capability into the client. Neither the outer helper environment nor generated
  policy/argv may contain the real value. Test all visible ancestors, not just
  the immediate provider parent, and keep the macOS process-inspection probe.
- Bind exact canonical API origins/paths/methods and capabilities to a trusted
  Run; retain DNS-address denial, upstream certificate verification, streaming
  and cancellation. A custom gateway receiving authentication is necessarily a
  trusted credential recipient. Do not add a TLS-verification bypass.
- Revoke capabilities and close proxy connections at termination, including
  cancellation before client start and failed provider-session resume. Test
  cross-Run replay, redirects and model-management endpoint rejection.
- Use a fresh explicitly selected credential-isolation namespace when migrating
  from direct-key sessions: old client files/history may already contain a real
  key. Preserve old storage outside the new client's grants; do not copy or erase
  it automatically. Resume remains supported within the new namespace.
- Specify and test bounded request/body/concurrency/lifetime limits. These are
  not a currency budget or proof of protection from resource exhaustion.

Do not extend this design to extracted Claude subscription OAuth tokens. The
[current Claude Code authentication and hosting rules](https://code.claude.com/docs/en/legal-and-compliance)
allow an end user to authenticate to the unmodified binary with their own
subscription, but restrict developers collecting/intermediating subscription
credentials or routing other users through an operator's subscription. Hosting
Claude Code also has end-user credential/billing conditions. Private use and a
multi-user service need distinct product decisions; using an API key alone does
not remove those hosting conditions. No subscription migration or broker was
implemented in the gateway change.

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

Feasibility review (2026-09-13): Linux has a documented process-tree backend in
[cgroup v2](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html),
including `memory.max`, `pids.max` and subtree kill. Node needs an operator-
delegated hierarchy and must place the launcher inside its group before it can
fork; moving only an already-running parent is insufficient. This is not yet
wired into Node. Persistent byte/inode quotas also need a supported, provisioned
filesystem backend, separate from the memory controller.

On macOS, [APFS volume quotas](https://support.apple.com/guide/disk-utility/add-delete-or-erase-apfs-volumes-dskua9e6a110/mac)
can bound volume storage, but do not give each directory/session its own limit.
The [documented process resource limits](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/setrlimit.2.html)
do not establish a hard aggregate memory limit for an arbitrary session process
tree. A matching native macOS backend has not been established; do not label
per-process limits or periodic monitoring as equivalent to Linux cgroups.

## Runtime upgrade gate

The embedded runner intentionally depends on the pinned runtime's internal
module/command format. Upgrade its lock, top-level pin and runner adapter as one
reviewed change; install with `npm ci --ignore-scripts`, and run real macOS/Linux
enforcement, credential probes, both provider continuations and concurrency.
Unknown outer command syntax is a startup error, not an unsandboxed fallback.
The binary package checksum covers the bundled lock/installer; installed files
and the configured runtime remain trusted inputs, not attested executables.
