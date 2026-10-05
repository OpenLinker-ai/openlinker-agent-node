#!/usr/bin/env python3
"""Verify a root-owned, allowlisted synthetic acceptance bundle before any writes."""
import base64
import gzip
import hashlib
import json
from pathlib import Path
import re
import sys

repository = Path(__file__).resolve().parent.parent
lock = json.loads((repository / 'tools/controlled-upgrade/fixture-lock.json').read_text())
archive = repository / 'tools/controlled-upgrade/root-fixture.json.gz'
expected = {
    'scripts/verify-node-controlled-upgrade.mjs',
    'scripts/verify-node-controlled-upgrade.test.mjs',
    'scripts/verify-node-direct-registration.mjs',
    'scripts/fixtures/node-controlled-upgrade-core_test.go',
    'scripts/fixtures/node-controlled-upgrade-provider.mjs',
    'scripts/fixtures/node-upgrade-old-binaries.json',
}
if len(sys.argv) != 2:
    raise SystemExit('supply one new extraction directory')
if hashlib.sha256(archive.read_bytes()).hexdigest() != lock['sha256']:
    raise SystemExit('root fixture hash mismatch')
bundle = json.loads(gzip.decompress(archive.read_bytes()))
if bundle['root_commit'] != lock['root_commit']:
    raise SystemExit('wrong root source commit')
if len(bundle['files']) != len(expected) or {f['path'] for f in bundle['files']} != expected:
    raise SystemExit('unexpected or missing fixture files')
if len(lock['files']) != len(expected) or {f['path'] for f in lock['files']} != expected:
    raise SystemExit('invalid fixture lock')
locked_files = {f['path']: f for f in lock['files']}
verified = []
for entry in bundle['files']:
    metadata = {k: v for k, v in entry.items() if k != 'content'}
    if metadata != locked_files[entry['path']] or entry['mode'] not in {'100644', '100755'}:
        raise SystemExit('fixture metadata mismatch')
    content = base64.b64decode(entry['content'], validate=True)
    blob = b'blob ' + str(len(content)).encode() + b'\0' + content
    if hashlib.sha256(content).hexdigest() != entry['sha256'] or hashlib.sha1(blob).hexdigest() != entry['git_blob']:
        raise SystemExit('fixture content mismatch')
    verified.append((entry, content))
# This root-owned file is already authenticated by both the bundle and file
# hashes. Bind the public checkout pin to the Core commit reviewed with it.
root_lock = json.loads(next(content for entry, content in verified
                            if entry['path'] == 'scripts/fixtures/node-upgrade-old-binaries.json'))
core_commit = lock.get('core_commit', '')
if not isinstance(core_commit, str) or not re.fullmatch(r'[a-f0-9]{40}', core_commit) or root_lock.get('core_commit') != core_commit:
    raise SystemExit('reviewed Core commit mismatch')
destination = Path(sys.argv[1]).resolve()
destination.mkdir(mode=0o700, parents=False, exist_ok=False)
for entry, content in verified:
    target = destination / entry['path']
    target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with target.open('xb') as output:
        output.write(content)
    target.chmod(0o755 if entry['mode'] == '100755' else 0o644)
print(json.dumps({'root_commit': lock['root_commit'], 'sha256': lock['sha256']}))
