# Security Policy

Do not open public issues for vulnerabilities.

Use GitHub private vulnerability reporting when available. Otherwise contact
the maintainers through the published OpenLinker security/support channel with
the affected commit, reproducible steps, impact, and whether a live token or
service is involved.

Security-sensitive areas include:

- runtime token storage and transmission
- localhost helper token scope
- adapter command execution
- A2A delegation from active runs
- workspace isolation for the Codex adapter

Never include real third-party secrets in public reports or test cases.

