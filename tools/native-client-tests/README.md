# Official-client acceptance fixtures

CI only. Install with `npm ci --ignore-scripts --no-audit --no-fund`.
The lock pins both clients and their platform packages. No end-user credentials
are used: tests construct private synthetic login homes and a loopback model
fixture, then exercise the actual tools. This is not an installer for Node users.

Set `OPENLINKER_TEST_NATIVE_CODEX_BIN` and `OPENLINKER_TEST_NATIVE_CLAUDE_BIN` to
the output of `node tools/native-client-tests/path.mjs codex` / `claude` and run
`scripts/test-native-session-isolation.sh`. Keep both macOS and Linux green when
changing either client version or the tool policy.
