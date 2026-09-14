import { copyFileSync, mkdirSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repository = resolve(dirname(fileURLToPath(import.meta.url)), '..');

// Called by release packaging, not Agent startup. Native mode now uses the
// installed official client sandbox; ship its setup guide, no obsolete runtime.
export function stageNativeSandbox(destination) {
  const root = resolve(destination);
  if (!statSync(root).isDirectory()) throw new Error('archive staging directory is required');
  mkdirSync(join(root, 'docs'));
  for (const name of ['native-session-isolation.md','native-session-isolation.zh-CN.md']) {
    copyFileSync(join(repository,'docs',name), join(root,'docs',name));
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.length !== 3) throw new Error('usage: node scripts/stage-native-sandbox.mjs <archive-directory>');
  stageNativeSandbox(process.argv[2]);
}
