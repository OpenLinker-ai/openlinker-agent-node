// Embedded into the Node executable. The external sandbox package is pinned;
// no npm resolution, personal settings, shell startup file or dynamic control
// channel is used during a Run.
import fs from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { spawn } from 'node:child_process';

// Decode only the exact argv quoting emitted by SRT 0.0.76's quote(). This is
// deliberately not a shell parser: expansions/operators/new quoting formats
// cause a closed failure instead of evaluating host-side command text.
export function quotedArgv(text) {
  const args = [];
  let value = '', active = false;
  for (let i = 0; i < text.length;) {
    const ch = text[i];
    if (ch === ' ') {
      if (active) args.push(value);
      value = ''; active = false; i++; continue;
    }
    if (ch === "'") {
      const end = text.indexOf("'", i + 1);
      if (end < 0) throw new Error('unclosed generated argument');
      value += text.slice(i + 1, end); active = true; i = end + 1; continue;
    }
    if (text.slice(i, i + 3) === `"'"`) {
      value += "'"; active = true; i += 3; continue;
    }
    if (!/[A-Za-z0-9_./:=@+,-]/.test(ch)) throw new Error('unsupported generated shell syntax');
    value += ch; active = true; i++;
  }
  if (active) args.push(value);
  return args;
}

export function hardenMacCommand(wrapped) {
  const argv = quotedArgv(wrapped);
  const i = argv.indexOf('/usr/bin/sandbox-exec');
  if (argv[0] !== 'env' || i < 1 || argv[i+1] !== '-p' || !argv[i+2].startsWith('(version 1)')) {
    throw new Error('unsupported generated sandbox command');
  }
  // One Seatbelt profile: macOS refuses applying a second sandbox to a process
  // already inside SRT. Append denials before the one sandbox-exec invocation.
  // Metadata on symlinks allows executable/OS path resolution, not reading
  // their targets. SRT already permits metadata on intermediate directories.
  argv[i+2] += `\n; OpenLinker native sessions do not use host-user IPC\n
(deny user-preference-read)
(deny ipc-posix-shm)
(deny ipc-posix-sem)
(allow file-read-metadata (vnode-type SYMLINK))
(deny mach-lookup (global-name "com.apple.securityd.xpc"))
(deny mach-lookup (global-name "com.apple.distributed_notifications@Uv3"))\n`;
  argv[0] = '/usr/bin/env';
  return argv;
}

async function main() {
  const [runtimeEntry, policy, bin, ...args] = process.argv.slice(2);
  if (!runtimeEntry || !policy || !bin) throw new Error('missing sandbox launch arguments');
  const [major,minor] = process.versions.node.split('.').map(Number);
  if (major < 20 || major === 20 && minor < 11) throw new Error('Node 20.11+ is required');
  const dir = path.dirname(runtimeEntry);
  const manifest = JSON.parse(fs.readFileSync(path.join(dir,'../package.json'),'utf8'));
  if (manifest.name !== '@anthropic-ai/sandbox-runtime' || manifest.version !== '0.0.76') throw new Error('sandbox runtime version mismatch');
  const { SandboxManager, SandboxRuntimeConfigSchema } = await import(pathToFileURL(path.join(dir,'index.js')).href);
  const { quote } = await import(pathToFileURL(path.join(dir,'utils/shell-quote.js')).href);
  try {
    const settings = SandboxRuntimeConfigSchema.parse(JSON.parse(fs.readFileSync(policy,'utf8')));
    await SandboxManager.initialize(settings);
    const wrapped = await SandboxManager.wrapWithSandbox(quote([bin,...args]), '/bin/bash');
    const argv = process.platform === 'darwin' ? hardenMacCommand(wrapped) : ['/bin/bash','-c',wrapped];
    const child = spawn(argv[0],argv.slice(1),{shell:false,stdio:'inherit'});
    const code = await new Promise((resolve,reject) => {
      child.once('error',reject);
      child.once('exit',(code,signal)=>resolve(signal ? 1 : code ?? 1));
    });
    process.exitCode = code;
  } finally {
    await SandboxManager.reset();
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(fs.realpathSync(process.argv[1])).href) {
  main().catch(() => {
    // Do not echo policy contents, credentials or private host paths.
    process.stderr.write('Native sandbox initialization or execution failed; no unsandboxed fallback was used.\n');
    process.exitCode = 1;
  });
}
