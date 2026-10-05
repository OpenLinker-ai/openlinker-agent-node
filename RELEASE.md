# Release Process

Chinese documentation: [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)

OpenLinker Agent Node releases are cut from `main` after CI and local release
gates pass. Agent Node versions its Adapter host, CLI, helper, and public A2A
surface; the reliable Runtime implementation is pinned from `openlinker-go`.
Document notable changes under `Unreleased` in `CHANGELOG.md`.

## Pre-Release Checklist

1. Confirm `README.md` and `README.zh-CN.md` both present Agent Node as a
   standalone bridge and public protocol-leaf owner over the Go SDK Runtime Worker, and that `CONTRIBUTING`,
   `SECURITY`, `SUPPORT`, and examples are current.
2. Confirm `CHANGELOG.md` describes Adapter, helper, CLI, public A2A, and pinned
   SDK integration changes.
3. Run `gofmt -w .`.
4. Run `go test ./...`.
5. Run `go build ./cmd/openlinker-agent-node`.
6. Run a current-source secret scan on a clean checkout, for example
   `gitleaks dir --redact .`.
7. Confirm generated artifacts, `.env` files, coverage output, local binaries,
   adapter logs, and private workspace files are not tracked.
8. Confirm Agent/helper token and mTLS examples use placeholders only.
9. Confirm `AgentNodeVersion`, the release tag, enrollment examples, and the
   pinned `openlinker-go` compatibility notes agree.
10. Run `node --test scripts/build-agent-node.test.mjs` and the compiled command
    tests. Package only through `scripts/build-agent-node.mjs`, which injects the
    exact tag/commit; preserve the six-platform matrix and adjacent checksums.
11. **Supported release gate:** run
    `node scripts/check-release-upgrade-readiness.mjs "$tag"` with the actual tag.
    The sole stable candidate is `v0.2.0`; canonical `v0.x.y-alpha.N`,
    `v0.x.y-beta.N` and `v0.x.y-rc.N` remain test prereleases. Other stable and
    v1+ tags, malformed tags and missing/extra arguments fail. No environment
    or workflow-input bypass exists. All six platform packages must pass; stable
    publication additionally requires the root-owned controlled-upgrade gate on
    the exact packaged Linux amd64 binary. Retain its report on the Release.
12. Record exact Core/SDK/Node versions. Same-identity changes are limited to the
    version pair and scope in [controlled upgrade](docs/controlled-upgrade.md),
    requiring Core v0.3.0/schema 095 and administrator-authorized drain, stop,
    upgrade/restart operation, start and activation. macOS arm64 is verified
    separately after downloading the release and is claimed only with that
    binary's recorded evidence. Uncovered old versions use the explicit fresh
    test enrollment procedure in [README.md](./README.md#candidate-build-identity-and-test-only-enrollment):
    settle Attempts and spool, stop without deleting old state, then use a new
    NodeID, new unbound active credential and new private SDK DataDir. Never
    silently replace versions, clear state or automatically roll back. Ordinary
    active same-version restart is separate from administrative drain recovery.
13. Before calling an environment verified, record real WebSocket and pull
    enrollment, task execution, ordinary clean shutdown/restart and a further
    task, not merely transport reconnect or source tests. Core-controlled
    upgrade extensions are not a prerequisite for new test enrollment. Source
    merge, release publication, deployment and online verification remain
    separate facts; these checks do not themselves authorize publication.

## Tagging

Native isolation is experimental and restricted to trusted callers. Archives
stage both host-auth native-isolation guides with the binary; the existing
checksum covers those files. They no longer include the old SRT installer.
Run `node --test scripts/stage-native-sandbox.test.mjs` before packaging.
The installed official client remains trusted and is not attested by the Node
binary. Current host-auth source has macOS/Ubuntu CI evidence recorded in
`docs/host-auth-acceptance.md`; verify the actual release commit separately.
Real shared-login refresh and resource quotas remain open; Node serial admission
is cooperative, not account-wide exclusion. Do not label a prerelease safe for
untrusted callers or count historical SRT CI as host-auth acceptance.

For the HOME-lock update, stop old `/tmp`-lock Node processes and settle/stop
remaining clients before upgrade. Old and new locations do not coordinate. Native
serial deployments should use capacity 1 and share the underlying HOME state
storage; separate mounts with identical pathnames are insufficient.

Include this migration note in the release: moving from whole-client SRT to
host-auth mode starts a fresh workspace/native session once, retaining old
directories without copying or deleting them. Subsequent Runs reuse the new
scope. Later Bash-default hardening does not reset an existing host-auth scope.
Core history and Runtime enrollment/version migration are separate contracts.

After explicit publication approval and passing CI, choose the supported unused
release tag. For the reviewed stable candidate:

```bash
tag=v0.2.0
node scripts/check-release-upgrade-readiness.mjs "$tag"
git tag "$tag"
git push origin "$tag"
```

Breaking changes must be called out in `CHANGELOG.md`. Stable tags other than
`v0.2.0` and all v1+ releases remain blocked. Stable v0.2.0 is published without
the prerelease flag; alpha/beta/rc releases retain it. Non-tag workflow runs still build exact `sha-...`
artifacts and adjacent checksums for CI; they do not publish a GitHub Release.
The builder itself also accepts development and other exact version identities
for tests; successful local packaging is not release authorization.
