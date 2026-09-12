import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync, realpathSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';
import { stageNativeSandbox } from './stage-native-sandbox.mjs';

test('binary archive carries the same dependency lock and explicit script-free installer', t => {
  const root = mkdtempSync(join(tmpdir(),'ol-sandbox-bundle-'));
  t.after(()=>rmSync(root,{recursive:true,force:true}));
  stageNativeSandbox(root);
  for(const file of ['package.json','package-lock.json','install.sh','README.md']) {
    assert.deepEqual(readFileSync(join(root,'native-sandbox',file)),readFileSync(resolve('tools/native-sandbox',file)));
  }
  assert.ok(readFileSync(join(root,'docs/native-session-isolation.zh-CN.md')).length);
  assert.throws(()=>stageNativeSandbox(root));
  const fake = join(root,'bin');
  mkdirSync(fake);
  writeFileSync(join(fake,'npm'),'#!/bin/sh\nprintf "%s\\n" "$@" > "$INSTALL_ARGS"\n',{mode:0o755});
  const log = join(root,'install-args');
  const result = spawnSync('/bin/sh',[join(root,'native-sandbox/install.sh')],{
    env:{...process.env,PATH:`${fake}:${process.env.PATH}`,INSTALL_ARGS:log},encoding:'utf8',timeout:10000,
  });
  assert.equal(result.status,0,result.stderr);
  assert.deepEqual(readFileSync(log,'utf8').trim().split('\n'),[
    'ci','--prefix',realpathSync(join(root,'native-sandbox')),'--ignore-scripts','--no-audit','--no-fund',
  ]);
});
