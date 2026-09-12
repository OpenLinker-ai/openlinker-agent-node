import { copyFileSync, mkdirSync, statSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const repository = resolve(dirname(fileURLToPath(import.meta.url)), '..');

// Called by release packaging, not Agent startup. Refuse an existing bundle
// rather than replacing a previously installed optional runtime.
export function stageNativeSandbox(destination) {
  const root = resolve(destination);
  if (!statSync(root).isDirectory()) throw new Error('archive staging directory is required');
  mkdirSync(join(root, 'native-sandbox'));
  mkdirSync(join(root, 'docs'), { recursive: true });
  for (const name of ['package.json','package-lock.json','install.sh','README.md']) {
    copyFileSync(join(repository,'tools/native-sandbox',name), join(root,'native-sandbox',name));
  }
  for (const name of ['native-session-isolation.md','native-session-isolation.zh-CN.md']) {
    copyFileSync(join(repository,'docs',name), join(root,'docs',name));
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.length !== 3) throw new Error('usage: node scripts/stage-native-sandbox.mjs <archive-directory>');
  stageNativeSandbox(process.argv[2]);
}
