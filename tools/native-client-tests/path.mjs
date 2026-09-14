import {createRequire} from 'node:module';
import {dirname,join} from 'node:path';
import {accessSync,constants} from 'node:fs';
const require=createRequire(new URL('./package.json',import.meta.url));
const pins=require('./package.json').dependencies;
const provider=process.argv[2];
let path;
if(provider==='codex') {
  const manifest=require('@openai/codex/package.json');
  if(manifest.version!==pins['@openai/codex']) throw Error('Codex fixture version mismatch');
  path=join(dirname(require.resolve('@openai/codex/package.json')),'bin/codex.js');
} else if(provider==='claude') {
  if(!['linux','darwin'].includes(process.platform)) throw Error('Native sandbox fixtures require macOS/Linux');
  const suffix=process.platform==='linux'&&!process.report.getReport().header.glibcVersionRuntime?'-musl':'';
  const name=`@anthropic-ai/claude-code-${process.platform}-${process.arch}${suffix}`;
  if(require(`${name}/package.json`).version!==pins['@anthropic-ai/claude-code']) throw Error('Claude fixture version mismatch');
  // npm --ignore-scripts intentionally leaves the wrapper placeholder intact.
  // Execute the integrity-checked optional native package directly.
  path=join(dirname(require.resolve(`${name}/package.json`)),'claude');
} else throw Error('Expected codex or claude');
accessSync(path,constants.X_OK);
process.stdout.write(path+'\n');
