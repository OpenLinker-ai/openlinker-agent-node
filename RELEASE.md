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
11. **Test-only prerelease gate:** run
    `node scripts/check-release-upgrade-readiness.mjs "$tag"` with the actual tag.
    Only canonical `v0.x.y-alpha.N`, `v0.x.y-beta.N`, and `v0.x.y-rc.N` pass;
    numeric components are nonnegative without leading zeroes. Missing/extra
    arguments, stable tags, v1+ tags and malformed tags fail. No workflow-input
    or environment variable bypass is provided. The workflow passes the real
    `GITHUB_REF_NAME` before tagged packaging, and creates or updates the GitHub
    Release as a **prerelease** only after all six platform jobs have passed.
12. Document the selected Core/SDK/Node versions and fresh enrollment procedure
    from [README.md](./README.md#candidate-build-identity-and-test-only-enrollment).
    This policy is only for test deployments without real users: settle the old
    Attempts and empty spool, stop the old process without deleting state, then
    use a new NodeID, new unbound active credential and new private SDK DataDir.
    Existing Node version replacement and automatic rollback are unsupported.
    The same exact version and DataDir can restart an active Node ordinarily;
    this is not administrative drain/activate recovery. Do not claim a general
    migration controller, an in-place upgrade or OS-level isolation.
13. Before calling an environment verified, record real WebSocket and pull
    enrollment, task execution, ordinary clean shutdown/restart and a further
    task, not merely transport reconnect or source tests. Core-controlled
    upgrade extensions are not a prerequisite for new test enrollment. Source
    merge, release publication, deployment and online verification remain
    separate facts; these checks do not themselves authorize publication.

## Tagging

Native isolation is experimental and restricted to callers trusted to receive
the provider model key. Archives now stage `native-sandbox/` (manifest, lock,
installer and instructions) and both native-isolation guides with the binary;
the existing archive checksum covers those files. Run
`node --test scripts/stage-native-sandbox.test.mjs` before packaging. Installed
runtime content is not attested by the binary. Credential proxying and resource
quotas remain open work; do not label a prerelease safe for untrusted callers.

After explicit publication approval, choose an unused canonical pre-1.0 test
prerelease tag (the value below is an example, not a reserved next version):

```bash
tag=v0.1.59-rc.1
node scripts/check-release-upgrade-readiness.mjs "$tag"
git tag "$tag"
git push origin "$tag"
```

Breaking changes must be called out in `CHANGELOG.md`. Stable pre-1.0 and v1+
releases remain blocked. Non-tag workflow runs still build exact `sha-...`
artifacts and adjacent checksums for CI; they do not publish a GitHub Release.
The builder itself also accepts development and other exact version identities
for tests; successful local packaging is not release authorization.
