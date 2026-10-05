# Controlled version changes / 受控版本变更

Agent Node v0.2.0 retains the existing Go SDK pin
`v0.2.0-rc8.0.20260914164420-63fc87d73406` (source `63fc87d73406`,
also tagged as SDK v0.2.0), matching CLI and Plugin.
It does not add a second Worker or change provider execution, session scope,
credentials, defaults or persistent paths. Core v0.3.0 / schema 095 owns the new
administrative operation and admission fences. Upgrade Core with a coordinated
maintenance window before attempting this Node operation.

Agent Node v0.2.0 保留现有 SDK 精确锁定，与 CLI、Plugin、旧包一致；
源码提交 `63fc87d73406` 同时已有 SDK v0.2.0 正式标签。
Node 不复制 Worker，不改变 Provider 执行、会话作用域、凭据、默认值或持久路径。
受控版本变更和 drain 后恢复归 Core v0.3.0（schema 095）授权；Core 须先完成协调维护升级。

## Tested scope

The release gate executes the published Node `v0.1.62-rc.1` and the exact clean
candidate `v0.2.0` binary independently, using the same SDK DataDir and Worker
identity across WebSocket, explicit pull and auto-to-pull fallback. Each transport
runs A→B, a failed B start before activation and retry, same-version B restart,
and B→A with increasing revisions/epochs, followed by real task/result ACKs.
The synthetic Claude protocol process verifies that provider session mapping and
its private session identifier persist. It performs no real model calls and
has native isolation explicitly off. This evidence does not certify arbitrary
old releases, host login state, sandbox profiles, or upgrades of installed
Claude/Codex clients. Native host-auth isolation keeps its documented experimental
status, independently of whether the Node package version has an rc suffix.

Root integration owns the runner, old-artifact hash lock and evidence format.
The Node release workflow must pass that executable compatibility gate before
publishing a stable package; editing a document or setting an environment flag
cannot replace it. Record the exact Core/Node commits and binary hashes from
that run. The pre-publication upgrade gate covers the exact Linux amd64 package.
macOS arm64 is verified separately using the downloaded release package; claim
that platform only after its exact binary evidence has been recorded. Other
distributed architectures have build/unit coverage and fresh enrollment, not a
claimed cross-version state matrix. Only v0.1.62-rc.1 is the supported predecessor;
the historical v0.1.62 tag and other older versions are outside this matrix.

发布门禁使用真实旧包与干净候选二进制，验证相同 DataDir、Worker 身份、三种传输下的
升级、激活前失败重试、同版本恢复和回退，并完成真实 Core 任务及结果 ACK。合成 Claude
进程验证私有会话映射复用；没有调用模型，也没有打开原生隔离。该矩阵不代表任意旧版本、
主机登录态、沙箱 Profile 或 Claude/Codex 客户端自身升级都兼容。普通六平台构建与原生
沙箱现有测试仍须通过，但不能替代跨版本持久状态矩阵。发布前门禁覆盖 Linux amd64；
macOS arm64 须下载正式包单独验收，记录准确二进制证据后才可宣称通过。旧版本只覆盖
v0.1.62-rc.1，不包含历史 v0.1.62 标签及其他旧版本。

## Procedure

1. Pin the exact supported binary pair and read Core's
   [controlled upgrade guide](https://github.com/OpenLinker-ai/openlinker-core/blob/v0.3.0/docs/runtime-node-upgrade.md).
   The target must keep the same wire contract and advertised feature set.
2. Drain the whole Node; finish every Attempt, event/result ACK and SDK spool
   entry under the existing identity. Stop all processes using its DataDir.
3. Use an administrator JWT to GET then POST
   `/api/v1/admin/runtime/nodes/:id/upgrade` with a new operation UUID,
   `kind=version_change`, the exact expected version/revision, target version and
   deadline. Runtime tokens and platform User Tokens cannot authorize it.
   For the same binary after drain, use `restart_after_drain` with equal versions.
4. Start the selected binary with the unchanged Node ID, credential and current
   DataDir. Verify each intended Worker attached in draining state at capacity
   zero, then activate through the existing Core Admin API. Verify a further task.
5. Before activation, failed starts may retry with new Session IDs/higher epochs
   while the permit is valid. On expiry, request a new operation; replaying the
   same UUID only returns the original receipt and never extends permission.
   After an ambiguous timeout, first replay the same request/UUID to reconcile.

Do not spoof an old version, clear spool, overwrite current state with a snapshot,
resurrect an old Session ID, or run two Workers on one DataDir. A downgrade is a
new Core-authorized operation, never an automatic rollback. If the old binary
cannot read current state, stop it and recover using a compatible binary through
the controlled flow. Do not roll back Core schema 095 to force a Node to start.

先排空并停机，再由管理员授权精确版本/revision 变更；原身份和当前 DataDir 保持不变。
新 Session 以更高 epoch、draining/零容量接入，核实后再激活。超时须先用原 operation ID
对账，过期须申请新操作。不得伪报旧版本、清空 spool、恢复旧快照或共用 DataDir 并行启动。
不在精确验证矩阵内的旧版本采用明确的新身份登记流程，不宣称原地迁移。

## Root-owned acceptance fixture

The cross-repository fixture is authored and reviewed in the integration root.
The public Node release consumes only an immutable, allowlisted synthetic test
bundle under `tools/controlled-upgrade/`, with the root commit, reviewed Core
commit, archive hash and each source file's Git blob and SHA-256 pinned. The
consumer verifies every file before extraction. It contains no deployment
configuration, credentials, real model calls or SDK Worker implementation.

The reusable CI workflow validates a clean PR binary; the release workflow
validates the exact packaged Linux amd64 binary and its checksum before stable
publication. The macOS arm64 downloaded release binary is checked separately
from the root after publication; its result is not a pre-publication gate. The
Linux report is retained as `controlled-upgrade.json` on the stable release. `auto` covers forced WebSocket failure and long-poll fallback;
it does not exercise a successful automatic WebSocket selection.
