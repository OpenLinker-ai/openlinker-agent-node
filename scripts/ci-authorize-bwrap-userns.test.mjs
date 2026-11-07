import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./ci-authorize-bwrap-userns.sh', import.meta.url));

// Models the runner's AppArmor policy: a distro bwrap profile (Ubuntu 26.04),
// the CI profile loaded by apparmor_parser (needed on Ubuntu 24.04), or both,
// which the kernel treats as conflicting attachments and applies neither.
const stubs = {
  sysctl: `[ -n "\${STUB_RESTRICT:-}" ] || exit 1; cat "$STUB_DIR/restrict"`,
  sudo: 'exec "$@"',
  apparmor_parser: `echo "$*" >> "$STUB_DIR/parser.log"; touch "$STUB_DIR/ci-profile"
[ -z "\${STUB_FLIP_RESTRICT:-}" ] || echo 0 > "$STUB_DIR/restrict"`,
  bwrap: `policy=\${STUB_DISTRO_PROFILE:-0}; [ -e "$STUB_DIR/ci-profile" ] && policy="\${policy}ci"
case "\${STUB_BROKEN:-}:\${STUB_RESTRICT:-}:$policy" in
  1:*|:1:0|:1:1ci) echo 'bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted' >&2; exit 1 ;;
  *:1) label='bwrap//&unpriv_bwrap (enforce)' ;;
  *:0ci) label='openlinker-ci-bwrap (unconfined)' ;;
  *) label=unconfined ;;
esac
case "$*" in *' /proc/self/attr/current') echo "$label" ;; esac`,
};

function run(env) {
  const dir = mkdtempSync(path.join(tmpdir(), 'ci-bwrap-'));
  try {
    for (const [name, body] of Object.entries(stubs)) {
      writeFileSync(path.join(dir, name), `#!/bin/sh\n${body}\n`);
      chmodSync(path.join(dir, name), 0o755);
    }
    writeFileSync(path.join(dir, 'restrict'), `${env.STUB_RESTRICT ?? ''}\n`);
    const result = spawnSync('bash', [script], {
      encoding: 'utf8',
      env: { PATH: `${dir}:/usr/bin:/bin`, STUB_DIR: dir, ...env },
    });
    const logFile = path.join(dir, 'parser.log');
    return { ...result, parser: existsSync(logFile) ? readFileSync(logFile, 'utf8').trim().split('\n') : [] };
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test('Ubuntu 26.04: keeps the distro bwrap profile and never adds a conflicting one', () => {
  const r = run({ STUB_RESTRICT: '1', STUB_DISTRO_PROFILE: '1' });
  assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(r.parser, []);
  assert.match(r.stdout, /bwrap\/\/&unpriv_bwrap \(enforce\)/);
});

test('Ubuntu 24.04: loads only the executable-specific CI profile once', () => {
  const r = run({ STUB_RESTRICT: '1' });
  assert.equal(r.status, 0, r.stderr);
  assert.deepEqual(r.parser, ['-r scripts/ci-bwrap.apparmor']);
  assert.match(r.stdout, /openlinker-ci-bwrap/);
});

test('no AppArmor userns restriction: loads nothing and keeps a working bwrap', () => {
  for (const restrict of ['0', undefined]) {
    const r = run(restrict === undefined ? {} : { STUB_RESTRICT: restrict });
    assert.equal(r.status, 0, r.stderr);
    assert.deepEqual(r.parser, []);
  }
});

test('fails without loading policy when bwrap is broken for another reason', () => {
  const r = run({ STUB_RESTRICT: '0', STUB_BROKEN: '1' });
  assert.notEqual(r.status, 0);
  assert.deepEqual(r.parser, []);
  assert.match(r.stderr, /Failed RTM_NEWADDR/);
});

test('fails if provisioning changes the global userns restriction', () => {
  const r = run({ STUB_RESTRICT: '1', STUB_FLIP_RESTRICT: '1' });
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /global unprivileged-userns restriction changed/);
});
