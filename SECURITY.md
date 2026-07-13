# Security Policy

Chinese documentation: [SECURITY.zh-CN.md](./SECURITY.zh-CN.md)

Do not open public issues for vulnerabilities.

Use GitHub private vulnerability reporting when available. If it is not
available, contact the maintainers through the published OpenLinker
security/support channel. Include the affected repository, commit or release,
reproduction steps, impact, and whether any live token, public endpoint, or
customer data is involved.

## Supported Versions

OpenLinker Agent Node is pre-1.0. Security fixes target the current `main`
branch and the latest tagged release when tags are available. Older commits may
not receive backports unless maintainers explicitly announce support for a
release line.

## Security-Sensitive Areas

- wiring Agent Token, mTLS paths, and the SDK file-store directory without
  exposing them to Adapters
- passing assignment-scoped capabilities only through SDK-owned calls
- localhost helper token scope
- adapter command execution and environment handling
- A2A delegation from active runs
- public A2A server request handling
- workspace isolation for the Codex adapter

The pinned `openlinker-go` SDK owns mTLS loading, Runtime transport and Session
state, encrypted journal/spool handling, lease fencing, resume, cancellation,
and duplicate-execution prevention. Report defects that reproduce in the SDK
to that repository; report leaks or unsafe wiring introduced by this host here.

## Reporting Guidance

Please include:

- the affected commit, tag, or binary version
- Runtime Session ID (redacted), Adapter mode, and whether the issue reproduces
  in `openlinker-go` directly
- adapter mode (`http`, `command`, `a2a`, `codex`, or other)
- a minimal reproduction and sanitized logs
- whether the issue exposes an Agent Token, invocation capability, mTLS key, or helper token

Never include real third-party secrets in public reports, tests, screenshots, or
logs. If a token was exposed, rotate it before sharing details.

## Disclosure

Maintainers will triage reports as quickly as practical. Please avoid public
disclosure until a fix, mitigation, or coordinated disclosure timeline is
available.
