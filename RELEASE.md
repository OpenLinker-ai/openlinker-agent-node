# Release Process

Chinese documentation: [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)

OpenLinker Agent Node releases are cut from `main` after CI and local release
gates pass. Until the runtime protocol and CLI behavior are stable enough for
strict semantic versioning, document notable changes under `Unreleased` in
`CHANGELOG.md`.

## Pre-Release Checklist

1. Confirm `README.md`, `CONTRIBUTING.md`, `SECURITY.md`, `SUPPORT.md`, and
   examples are current.
2. Confirm `CHANGELOG.md` describes runtime, adapter, helper, and CLI changes.
3. Run `gofmt -w .`.
4. Run `go test ./...`.
5. Run `go build ./cmd/openlinker-agent-node`.
6. Run a current-source secret scan on a clean checkout, for example
   `gitleaks dir --redact .`.
7. Confirm generated artifacts, `.env` files, coverage output, local binaries,
   adapter logs, and private workspace files are not tracked.
8. Confirm Agent/helper token and mTLS examples use placeholders only.

## Tagging

Use semantic version tags when maintainers publish versioned binaries:

```bash
git tag v0.x.y
git push origin v0.x.y
```

Pre-1.0 releases may include breaking changes, but they must be called out in
`CHANGELOG.md`.
