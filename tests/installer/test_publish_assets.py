"""Exercise real Git publication against disposable local bare repositories."""
import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[2]
PUBLISHER = REPO/'scripts/publish-assets.sh'
NAMES = ('shell-wol-darwin-amd64', 'shell-wol-darwin-arm64',
         'shell-wol-windows-amd64.exe', 'shell-wol-windows-arm64.exe',
         'shell-wol-linux-amd64', 'shell-wol-linux-arm64', 'shell-wol-linux-arm-7',
         'shell-wol-linux-mips-softfloat', 'shell-wol-linux-mipsle-softfloat')


class PublishAssetsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='wol-publish-')
        self.base = Path(self.temp.name)
        self.remote = self.base/'remote.git'
        self.repo = self.base/'source'; self.repo.mkdir()
        self.env = dict(os.environ, GIT_ALLOW_PROTOCOL='file', GIT_TERMINAL_PROMPT='0')
        self.git('init', '--bare', str(self.remote))
        self.git('init', '-b', 'main')
        self.git('config', 'user.name', 'Test'); self.git('config', 'user.email', 'test@example.invalid')
        self.git('remote', 'add', 'origin', str(self.remote))
        (self.repo/'README.md').write_text('source repository\n')
        self.git('add', '.'); self.git('commit', '-m', 'source')
        self.git('push', 'origin', 'main')
        self.head = self.git('rev-parse', 'HEAD')
        self.directory = self.base/'artifacts'; self.directory.mkdir()
        self.assets('v1.2.3')

    def tearDown(self): self.temp.cleanup()

    def git(self, *args):
        r = subprocess.run(['git', *args], cwd=self.repo, env=self.env,
                           text=True, capture_output=True)
        self.assertEqual(r.returncode, 0, r.stdout+r.stderr)
        return r.stdout.strip()

    def ref(self, name):
        return self.git('ls-remote', '--refs', 'origin', name).split('\t')[0]

    def assets(self, version):
        (self.directory/'VERSION').write_text(version+'\n')
        for name in NAMES: (self.directory/name).write_bytes((version+' '+name+'\n').encode())
        self.manifest()

    def manifest(self):
        (self.directory/'SHA256SUMS').write_text(''.join(
            hashlib.sha256((self.directory/name).read_bytes()).hexdigest()+'  '+name+'\n'
            for name in NAMES))

    def publish(self, action='snapshot', version='v1.2.3', success=True):
        args = ['bash', str(PUBLISHER), action, version]
        if action == 'snapshot': args.append(str(self.directory))
        r = subprocess.run(args, cwd=self.repo, env=self.env,
                           text=True, capture_output=True, timeout=20)
        self.assertEqual(r.returncode == 0, success, r.stdout+r.stderr)
        return r

    def test_snapshot_preserves_source_and_uploads_exact_files(self):
        # Keep both staged and unstaged source edits; publisher must use a private index.
        (self.repo/'staged').write_text('user edit'); self.git('add', 'staged')
        (self.repo/'README.md').write_text('uncommitted source edit\n')
        before = self.git('status', '--porcelain')
        (self.directory/'unrelated').write_text('never publish')
        self.publish()
        tag = self.ref('refs/tags/assets-v1.2.3')
        self.assertTrue(tag); self.assertEqual(self.ref('refs/heads/main'), self.head)
        self.assertEqual(self.git('rev-parse', 'HEAD'), self.head)
        self.assertEqual(self.git('status', '--porcelain'), before)
        files = self.git('--git-dir='+str(self.remote), 'ls-tree', '--name-only', tag).splitlines()
        self.assertEqual(set(files), set(NAMES)|{'VERSION', 'SHA256SUMS', '.shell-wol-assets'})
        for name in NAMES:
            self.assertEqual(self.git('--git-dir='+str(self.remote), 'show', tag+':'+name),
                             (self.directory/name).read_text().strip())
        self.assertEqual(self.ref('refs/heads/assets'), '')

    def test_snapshot_is_idempotent_and_immutable(self):
        self.publish(); original = self.ref('refs/tags/assets-v1.2.3')
        self.publish(); self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), original)
        (self.directory/NAMES[0]).write_text('different valid build'); self.manifest()
        self.publish(success=False)
        self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), original)

    def test_invalid_download_cannot_create_a_tag(self):
        (self.directory/NAMES[0]).write_text('corrupt file')
        self.publish(success=False)
        self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), '')

    def test_missing_asset_and_symlink_are_rejected(self):
        p = self.directory/NAMES[0]; p.unlink()
        self.publish(success=False)
        outside = self.base/'outside'; outside.write_text('not an asset')
        p.symlink_to(outside); self.publish(success=False)
        self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), '')

    def test_duplicate_and_unknown_manifest_paths_are_rejected(self):
        manifest = self.directory/'SHA256SUMS'; before = manifest.read_text()
        manifest.write_text(before+before.splitlines()[0]+'\n')
        self.publish(success=False)
        manifest.write_text(before+'0'*64+'  ../outside\n')
        self.publish(success=False)
        self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), '')

    def test_file_size_limit_is_checked_before_publication(self):
        with (self.directory/NAMES[0]).open('wb') as f: f.truncate(21*1024*1024)
        self.manifest(); result = self.publish(success=False)
        self.assertIn('20 MB', result.stderr)
        self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), '')

    def test_latest_uses_snapshot_and_never_downgrades_on_backfill(self):
        self.publish(); self.publish('latest')
        first = self.ref('refs/heads/assets')
        self.assertEqual(first, self.ref('refs/tags/assets-v1.2.3'))
        self.assets('v1.2.10'); self.publish(version='v1.2.10'); self.publish('latest', 'v1.2.10')
        newer = self.ref('refs/heads/assets'); self.assertNotEqual(first, newer)
        self.publish('latest', 'v1.2.3')
        self.assertEqual(self.ref('refs/heads/assets'), newer)
        self.assertEqual(self.ref('refs/tags/assets-v1.2.3'), first)
        self.assertEqual(self.ref('refs/heads/main'), self.head)

    def test_prerelease_and_missing_snapshot_do_not_change_latest(self):
        self.assets('v1.2.4-beta.1'); self.publish(version='v1.2.4-beta.1')
        self.publish('latest', 'v1.2.4-beta.1')
        self.assertEqual(self.ref('refs/heads/assets'), '')
        self.publish('latest', 'v1.2.3', success=False)
        self.assertEqual(self.ref('refs/heads/assets'), '')

    def test_unmanaged_assets_branch_is_never_overwritten(self):
        self.publish()
        self.git('push', 'origin', self.head+':refs/heads/assets')
        self.publish('latest', success=False)
        self.assertEqual(self.ref('refs/heads/assets'), self.head)


if __name__ == '__main__': unittest.main(verbosity=2)
