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
11. **Current candidate release blocker:** do not publish this version-injection
    change before the supported Core-controlled version upgrade/rollback path
    has been delivered and verified for existing enrollments. Record its exact
    Core release and user-facing procedure. Neither warnings nor a new Node ID
    chosen for a private migration satisfy this product compatibility gate.
    Include clean shutdown/restart on both WebSocket and pull, not just network
    reconnect. Do not publish instructions for a command that does not exist.
    Tagged packaging currently executes `scripts/check-release-upgrade-readiness.mjs`
    and deliberately fails. There is no workflow-input/environment bypass;
    replace this block only with the reviewed Core compatibility integration.

## Tagging

Use semantic version tags when maintainers publish versioned binaries:

```bash
git tag v0.x.y
git push origin v0.x.y
```

Pre-1.0 releases may include breaking changes, but they must be called out in
`CHANGELOG.md`.
