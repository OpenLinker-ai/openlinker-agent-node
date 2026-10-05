import base64
import copy
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

PREPARE = Path(__file__).with_name('prepare-controlled-upgrade.py')
PATHS = [
    'scripts/verify-node-controlled-upgrade.mjs',
    'scripts/verify-node-controlled-upgrade.test.mjs',
    'scripts/verify-node-direct-registration.mjs',
    'scripts/fixtures/node-controlled-upgrade-core_test.go',
    'scripts/fixtures/node-controlled-upgrade-provider.mjs',
    'scripts/fixtures/node-upgrade-old-binaries.json',
]

def fixture():
    files = []
    for path in PATHS:
        content = ('synthetic ' + path).encode()
        if path.endswith('node-upgrade-old-binaries.json'):
            content = json.dumps({'core_commit': 'c'*40}).encode()
        blob = b'blob ' + str(len(content)).encode() + b'\0' + content
        files.append(dict(path=path, mode='100755' if 'provider.mjs' in path else '100644',
                          git_blob=hashlib.sha1(blob).hexdigest(), sha256=hashlib.sha256(content).hexdigest(),
                          content=base64.b64encode(content).decode()))
    return dict(root_commit='a'*40, files=files)

class Preparation(unittest.TestCase):
    def run_case(self, mutate=lambda bundle, lock: None):
        with tempfile.TemporaryDirectory(prefix='node-fixture-test-') as directory:
            root = Path(directory)
            (root / 'scripts').mkdir()
            (root / 'tools/controlled-upgrade').mkdir(parents=True)
            shutil.copyfile(PREPARE, root / 'scripts/prepare-controlled-upgrade.py')
            bundle = fixture()
            lock = dict(root_commit=bundle['root_commit'], core_commit='c'*40,
                        files=[{k:v for k,v in f.items() if k != 'content'} for f in bundle['files']])
            mutate(bundle, lock)
            archive = gzip.compress(json.dumps(bundle).encode())
            lock.setdefault('sha256', hashlib.sha256(archive).hexdigest())
            (root / 'tools/controlled-upgrade/root-fixture.json.gz').write_bytes(archive)
            (root / 'tools/controlled-upgrade/fixture-lock.json').write_text(json.dumps(lock))
            target = root / 'output'
            result = subprocess.run([sys.executable, str(root / 'scripts/prepare-controlled-upgrade.py'), str(target)], capture_output=True)
            if result.returncode == 0:
                self.assertEqual({p.relative_to(target).as_posix() for p in target.rglob('*') if p.is_file()}, set(PATHS))
                self.assertEqual((target / PATHS[4]).stat().st_mode & 0o777, 0o755)
            else:
                self.assertFalse(target.exists(), 'all validation must precede writes')
            return result.returncode

    def test_valid_export_and_executable_provider(self):
        self.assertEqual(self.run_case(), 0)

    def test_corrupt_hash_and_unreviewed_content_rejected(self):
        mutations = [
            lambda b,l: l.update(sha256='0'*64),
            lambda b,l: b.update(root_commit='b'*40),
            lambda b,l: b['files'][0].update(content=base64.b64encode(b'changed').decode()),
            lambda b,l: b['files'][0].update(mode='120000'),
            lambda b,l: b['files'].pop(),
            lambda b,l: b['files'].append(copy.deepcopy(b['files'][0])),
            lambda b,l: b['files'][0].update(path='../escaped'),
            lambda b,l: b['files'][0].update(path='/tmp/escaped'),
            lambda b,l: l['files'][0].update(git_blob='0'*40),
            lambda b,l: l.update(core_commit='d'*40),
            lambda b,l: l.pop('core_commit'),
            lambda b,l: l.update(core_commit='main'),
        ]
        for mutate in mutations:
            with self.subTest(mutate=mutate):
                self.assertNotEqual(self.run_case(mutate), 0)

    def test_matching_metadata_cannot_authorize_invalid_mode_or_content(self):
        for field, value in [('mode', '120000'), ('sha256', '0'*64), ('git_blob', '0'*40)]:
            def mutate(bundle, lock):
                bundle['files'][0][field] = value
                lock['files'][0][field] = value
            with self.subTest(field=field):
                self.assertNotEqual(self.run_case(mutate), 0)

if __name__ == '__main__':
    unittest.main()
