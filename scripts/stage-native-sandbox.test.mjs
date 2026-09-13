import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync, existsSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { stageNativeSandbox } from './stage-native-sandbox.mjs';

test('binary archive carries current native client instructions without the obsolete SRT installer', t => {
  const root = mkdtempSync(join(tmpdir(),'ol-native-guide-'));
  t.after(()=>rmSync(root,{recursive:true,force:true}));
  stageNativeSandbox(root);
  for(const file of ['native-session-isolation.md','native-session-isolation.zh-CN.md']) {
    assert.deepEqual(readFileSync(join(root,'docs',file)),readFileSync(resolve('docs',file)));
  }
  assert.equal(existsSync(join(root,'native-sandbox')),false);
  assert.throws(()=>stageNativeSandbox(root));
});
