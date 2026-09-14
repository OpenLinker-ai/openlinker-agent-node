# Deprecated whole-client SRT fixture

This locked runtime is retained for the experimental `sessionsandbox.Open` /
`Session.Command` compatibility API and its historical regression tests only.
Current Node native mode uses the installed official client’s tool sandbox;
it does not require this package, ship this installer in its binary archives,
or accept `OPENLINKER_AGENT_NODE_SESSION_SANDBOX_BIN`.

To run the retained leaf tests, explicitly install here with
`npm ci --ignore-scripts --no-audit --no-fund` and set the test-only
`OPENLINKER_TEST_NATIVE_SANDBOX_BIN` to `node_modules/.bin/srt` (absolute path).
The adjacent lock pins direct/transitive versions and tarball integrity.
No installer starts an Agent or modifies OS policy.

Do not add new product callers. Removal of this fixture, the old runner/staging
code and dedicated CI tests must be coordinated with a pre-1.0 API compatibility
announcement. Keep the current official-client OS acceptance tests.
See the [follow-up plan](../../docs/native-session-isolation-follow-ups.md).

中文：仅保留旧 SRT 实验 API 的兼容回归；当前 Node 原生模式不使用此包。
不要按旧说明给 Node 配置 SRT。删除需与公开 API、旧打包脚本和 CI 同步，
当前真实客户端的系统沙箱验收继续保留。
