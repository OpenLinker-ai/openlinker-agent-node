import { test } from 'node:test';
import assert from 'node:assert/strict';
import { quotedArgv, hardenMacCommand } from '../pkg/adapters/sessionsandbox/runner.mjs';

const quote = args => args.map(s => `'${s.replaceAll("'", `'"'"'`)}'`).join(' ');
test('generated argv preserves quotes, newlines and shell-looking bytes without evaluation', () => {
  const args = ['env','A=value','/usr/bin/sandbox-exec','-p','(version 1)\n(deny default)', '/bin/bash','-c', `printf '%s' '$(touch INJECTED)' "a ! b"`, '', "x\ny"];
  assert.deepEqual(quotedArgv(quote(args)),args);
  for (const value of ['env x; touch bad','env $(id)', 'env "$HOME"', "env 'unclosed", 'env x\ny']) {
    assert.throws(()=>quotedArgv(value));
  }
});
test('macOS adds IPC denials and symlink metadata to one original Seatbelt profile',()=>{
  const original='(version 1)\n(deny default)\n(allow process-exec)';
  const argv=hardenMacCommand(quote(['env','HTTP_PROXY=http://127.0.0.1:123','/usr/bin/sandbox-exec','-p',original,'/bin/bash','-c','echo ok']));
  assert.equal(argv[0],'/usr/bin/env');
  assert.ok(argv[4].startsWith(original));
  assert.match(argv[4],/\(deny user-preference-read\)/);
  assert.match(argv[4],/\(deny ipc-posix-shm\)/);
  assert.match(argv[4],/com\.apple\.securityd\.xpc/);
  assert.match(argv[4],/\(allow file-read-metadata \(vnode-type SYMLINK\)\)/);
  assert.equal(argv.filter(v=>v==='/usr/bin/sandbox-exec').length,1);
  assert.deepEqual(argv.slice(5),['/bin/bash','-c','echo ok']);
  assert.throws(()=>hardenMacCommand('echo bypass'));
});
