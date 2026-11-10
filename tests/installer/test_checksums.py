"""SHA256 selection and failure handling across standalone deployment scripts."""
import hashlib
import json
import shlex
import shutil
import subprocess
import unittest

import test_install as fixture


BACKENDS = ('sha256sum', 'busybox', 'shasum', 'openssl')
BACKEND = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
name=pathlib.Path(sys.argv[0]).name
args={'sha256sum':[], 'busybox':['sha256sum'], 'shasum':['-a','256'],
      'openssl':['dgst','-sha256']}
assert sys.argv[1:]==args[name],sys.argv
with (pathlib.Path(os.environ['FIXTURE_ROOT'])/'checksum-calls').open('a') as f:f.write(name+'\n')
mode=json.loads(os.environ['CHECKSUM_MODES']).get(name,'unavailable')
if mode=='unavailable':sys.exit(127)
data=sys.stdin.buffer.read()
digest=hashlib.sha256(data).hexdigest()
if mode=='wrong':digest='0'*64
if mode=='malformed' or (mode=='bad-file' and data):digest='not-a-digest'
if mode=='upper':digest=digest.upper()
print(('SHA2-256(stdin)= '+digest) if name=='openssl' else (digest+'  -'))
if mode=='multiline':print('unexpected extra output')
if mode=='failed-output' or (mode=='failed-file' and data):sys.exit(1)
'''


def configure_backends(f, env, modes):
    # Shadow all host backends: unavailable applets return 127, as BusyBox does.
    for name in BACKENDS: f.tool(name, BACKEND)
    env['CHECKSUM_MODES'] = json.dumps(modes)
    trace = f.base/'checksum-calls'
    trace.unlink(missing_ok=True)
    return trace


class ChecksumTests(unittest.TestCase):
    def setUp(self):
        self.f = fixture.InstallerTests(); self.f.setUp()
        self.env = self.f.env.copy()
        self.env['SHELL_WOL_UNINSTALL_LIBRARY'] = '1'
        self.input = self.f.base/'File With Spaces'
        self.input.write_bytes(b'\x00\xffShell-WoL checksum\n')
        self.expected = hashlib.sha256(self.input.read_bytes()).hexdigest()
        boot = (fixture.REPO/'scripts/boot.sh').read_text()
        start = boot.index('# BEGIN SHA256 compatibility')
        end = boot.index('# END SHA256 compatibility')+len('# END SHA256 compatibility')
        self.boot_functions = self.f.base/'boot-checksum-functions.sh'
        self.boot_functions.write_text(boot[start:end]+'\n')
        self.scripts = (fixture.INSTALLER, fixture.REPO/'scripts/uninstall.sh', self.boot_functions)

    def tearDown(self): self.f.tearDown()

    def compute(self, script, modes, success=True, input=None):
        trace = configure_backends(self.f, self.env, modes)
        command = '. "$1"; require_sha256 || exit 1; echo "$SHA256_TOOL"; checksum "$2"'
        r = subprocess.run(['sh', '-c', command, 'checksum', str(script), str(input or self.input)],
                           env=self.env, text=True, capture_output=True)
        self.assertEqual(r.returncode == 0, success, r.stdout+r.stderr)
        return r, trace.read_text().splitlines()

    def test_preference_order_and_all_fallbacks(self):
        for script in self.scripts:
            for index, chosen in enumerate(BACKENDS):
                with self.subTest(script=script.name, chosen=chosen):
                    modes = {name: 'ok' for name in BACKENDS[index:]}
                    r, calls = self.compute(script, modes)
                    self.assertEqual(r.stdout.splitlines(), [chosen, self.expected])
                    self.assertEqual(calls, list(BACKENDS[:index+1])+[chosen])

    def test_broken_backends_are_skipped_even_when_output_looks_valid(self):
        for script in self.scripts:
            for broken in ('wrong', 'malformed', 'failed-output', 'multiline'):
                with self.subTest(script=script.name, broken=broken):
                    r, _ = self.compute(script, {'sha256sum': broken, 'busybox': 'ok'})
                    self.assertEqual(r.stdout.splitlines(), ['busybox', self.expected])

    def test_uppercase_digests_are_normalized(self):
        for script in self.scripts:
            for chosen in BACKENDS:
                with self.subTest(script=script.name, chosen=chosen):
                    r, _ = self.compute(script, {chosen: 'upper'})
                    self.assertEqual(r.stdout.splitlines(), [chosen, self.expected])

    def test_all_unavailable_fail_with_actionable_message(self):
        for script in self.scripts:
            with self.subTest(script=script.name):
                r, calls = self.compute(script, {}, success=False)
                self.assertEqual(calls, list(BACKENDS))
                self.assertEqual(r.stdout, '')
                for name in ('SHA256', 'busybox sha256sum', 'shasum -a 256', 'openssl dgst -sha256'):
                    self.assertIn(name, r.stderr)

    def test_real_file_failures_are_not_masked_or_retried_with_weaker_checks(self):
        for script in self.scripts:
            for failure in ('failed-file', 'bad-file'):
                with self.subTest(script=script.name, failure=failure):
                    r, calls = self.compute(script, {'sha256sum': failure, 'busybox': 'ok'}, success=False)
                    self.assertEqual(r.stdout.splitlines(), ['sha256sum'])
                    self.assertEqual(calls, ['sha256sum', 'sha256sum'])
            with self.subTest(script=script.name, missing=True):
                self.compute(script, {'openssl': 'ok'}, success=False, input=self.f.base/'missing')

    def test_fallback_still_rejects_corrupted_download_before_install(self):
        configure_backends(self.f, self.f.env, {'busybox': 'ok'})
        self.f.run_install()
        before = (self.f.root/'bin/shell-wol').read_bytes()
        (self.f.assets/'shell-wol-linux-amd64').write_text('damaged binary')
        r = self.f.run_install(success=False)
        self.assertIn('SHA256 校验失败', r.stderr)
        self.assertEqual((self.f.root/'bin/shell-wol').read_bytes(), before)

    def test_missing_backends_stop_install_and_uninstall_before_changes(self):
        configure_backends(self.f, self.f.env, {})
        r = self.f.run_install(success=False)
        self.assertIn('没有可用的 SHA256', r.stderr)
        self.assertFalse(self.f.root.exists())
        configure_backends(self.f, self.f.env, {'busybox': 'ok'})
        self.f.run_install()
        before = {str(p): p.read_bytes() for p in self.f.root.rglob('*') if p.is_file()}
        configure_backends(self.f, self.env, {})
        self.env['SHELL_WOL_UNINSTALL_LIBRARY'] = '0'
        r = subprocess.run(['sh', str(fixture.REPO/'scripts/uninstall.sh'), '--user',
                            '--install-dir', str(self.f.root), '--purge', '--yes'],
                           env=self.env, text=True, capture_output=True)
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('没有可用的 SHA256', r.stderr)
        self.assertEqual({str(p): p.read_bytes() for p in self.f.root.rglob('*') if p.is_file()}, before)
        self.assertFalse((self.f.root/'.install-lock').exists())

    @unittest.skipUnless(shutil.which('openssl'), 'OpenSSL is not installed')
    def test_real_openssl_output_is_supported(self):
        configure_backends(self.f, self.env, {})
        self.f.tool('openssl', '#!/bin/sh\nexec '+shlex.quote(shutil.which('openssl'))+' "$@"\n')
        for script in self.scripts:
            with self.subTest(script=script.name):
                r = subprocess.run(['sh', '-c', '. "$1"; require_sha256 && checksum "$2"',
                                    'checksum', str(script), str(self.input)],
                                   env=self.env, text=True, capture_output=True)
                self.assertEqual(r.returncode, 0, r.stdout+r.stderr)
                self.assertEqual(r.stdout.strip(), self.expected)


if __name__ == '__main__': unittest.main(verbosity=2)
