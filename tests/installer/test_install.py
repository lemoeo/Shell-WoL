"""Installer transaction tests. Downloads and service managers are isolated fixtures.
Only the Python test runner is needed; installation itself has no Python dependency.
"""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]
INSTALLER = REPO / 'scripts/install.sh'

BINARY = r'''#!/bin/sh
version=VERSION_VALUE
[ "$1" != version ] || { printf '%s\n' "$version"; exit 0; }
command=$1; shift
required=0; running=0
while [ "$#" -gt 0 ]; do
 case "$1" in
 --data-dir) data=$2; shift 2;;
 --require-password) required=1; shift;;
 --running) running=1; shift;;
 *) exit 2;;
 esac
done
if [ "$command" = init ]; then
 mkdir -p "$data"
 [ -f "$data/config.yaml" ] || printf 'password_hash: ""\nlog_limit: 100\n' > "$data/config.yaml"
 [ -f "$data/devices.yaml" ] || printf 'devices: []\n' > "$data/devices.yaml"
 exit 0
fi
[ "$command" = check ] || exit 3
[ -f "$data/config.yaml" ] && [ -f "$data/devices.yaml" ] || exit 4
[ ! -f "$data/invalid" ] || exit 5
if [ "$required" = 1 ]; then grep -q 'password_hash: ready' "$data/config.yaml" || exit 6; fi
if [ "$running" = 1 ] && [ "${FAIL_RUNNING_VERSION:-}" = "$version" ]; then exit 7; fi
'''
CURL = r'''#!/usr/bin/env python3
import json, os, pathlib, shutil, sys
args=sys.argv[1:]
assert '--proto' in args and args[args.index('--proto')+1]=='=https'
assert '--proto-redir' in args and args[args.index('--proto-redir')+1]=='=https'
url=args[-1]; assert url.startswith('https://')
root=pathlib.Path(os.environ['FIXTURE_ROOT'])
with (root/'downloads').open('a') as log:log.write(url+'\n')
out=pathlib.Path(args[args.index('--output')+1])
if any(url.startswith(prefix) for prefix in json.loads(os.environ.get('FAIL_URL_PREFIXES','[]'))):
 out.write_text('partial failed download');sys.exit(22)
release=os.environ['FIXTURE_RELEASE_BASE'].rstrip('/')+'/'
repo=os.environ['FIXTURE_REPO_BASE'].rstrip('/')+'/'
cdn=os.environ.get('FIXTURE_CDN_BASE','https://cdn.jsdelivr.net').rstrip('/')+'/gh/lemoeo/Shell-WoL@'
if url.startswith(release):
 tail=url[len(release):].split('/')
 if tail==['latest','download','VERSION']:source=root/'assets'/'VERSION'
 elif len(tail)==3 and tail[0]=='download':source=root/'releases'/tail[1]/tail[2]
 else:sys.exit(22)
elif url.startswith(repo):
 tail=url[len(repo):].split('/')
 if len(tail)==3 and tail[1] in ('deploy','scripts'):source=root/'repository'/tail[0]/tail[1]/tail[2]
 else:sys.exit(22)
elif url.startswith(cdn):
 tail=url[len(cdn):].split('/')
 if tail==['assets','VERSION']:
  version=os.environ.get('CDN_VERSION')
  source=(root/'releases'/version/'VERSION') if version else root/'assets'/'VERSION'
 elif len(tail)==2 and tail[0].startswith('assets-v'):source=root/'releases'/tail[0][7:]/tail[1]
 elif len(tail)==3 and tail[1] in ('deploy','scripts'):source=root/'repository'/tail[0]/tail[1]/tail[2]
 else:sys.exit(22)
 if os.environ.get('CORRUPT_CDN_SUFFIX') and url.endswith(os.environ['CORRUPT_CDN_SUFFIX']):
  out.write_text('corrupt CDN response');sys.exit(0)
else:sys.exit(22)
if not source.is_file():sys.exit(22)
shutil.copyfile(source,out)
'''
SYSTEMCTL = r'''#!/bin/sh
folder=$FIXTURE_SERVICE
printf '%s\n' "$*" >> "$folder/calls"
case "$1" in
is-active) [ -f "$folder/active" ];;
is-enabled) [ -f "$folder/enabled" ];;
start) touch "$folder/active";;
stop) rm -f "$folder/active";;
enable) touch "$folder/enabled";;
disable) rm -f "$folder/enabled";;
daemon-reload) [ "${FAIL_RELOAD:-0}" != 1 ];;
*) exit 2;;
esac
'''

class InstallerTests(unittest.TestCase):
    def setUp(self):
        # The production installer intentionally rejects volatile /tmp roots.
        self.temp = tempfile.TemporaryDirectory(prefix='wol-installer-', dir=REPO.parent)
        self.base = Path(self.temp.name)
        self.assets = self.base/'assets'
        self.release_versions=self.base/'releases';self.release_versions.mkdir()
        self.tools = self.base/'tools'; self.tools.mkdir()
        self.root = self.base/'Install With Spaces'
        self.service = self.base/'service'; self.service.mkdir()
        self.home=self.base/'home';self.home.mkdir()
        self.env = os.environ.copy()
        self.env.update(PATH=str(self.tools)+os.pathsep+self.env['PATH'], HOME=str(self.home), SHELL='/bin/sh', FIXTURE_ROOT=str(self.base), FIXTURE_RELEASE_BASE='https://github.com/lemoeo/Shell-WoL/releases', FIXTURE_REPO_BASE='https://raw.githubusercontent.com/lemoeo/Shell-WoL', FIXTURE_SERVICE=str(self.service), SHELL_WOL_INSTALL_LIBRARY='1')
        self.tool('curl', CURL)
        self.tool('systemctl', SYSTEMCTL)
        self.tool('sleep', '#!/bin/sh\nexit 0\n')
        self.make_release('v1.2.3')

    def tearDown(self): self.temp.cleanup()
    def tool(self,name,text):
        p=self.tools/name; p.write_text(text);p.chmod(0o755)
    def make_release(self,version):
        release=self.release_versions/version;release.mkdir(exist_ok=True)
        if self.assets.is_symlink():self.assets.unlink()
        self.assets.symlink_to(release,target_is_directory=True)
        (self.assets/'VERSION').write_text(version+'\n')
        p=self.assets/'shell-wol-linux-amd64';p.write_text(BINARY.replace('VERSION_VALUE',version));p.chmod(0o755)
        self.template_dir=self.base/'repository'/version/'deploy';self.template_dir.mkdir(parents=True,exist_ok=True)
        for name in ('shell-wol.service','shell-wol.openwrt','shell-wol.plist'):
            shutil.copyfile(REPO/'deploy'/name,self.template_dir/name)
        (self.template_dir/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in sorted(self.template_dir.iterdir()) if p.name!='SHA256SUMS'))
        self.menu_dir=self.base/'repository/main/scripts';self.menu_dir.mkdir(parents=True,exist_ok=True)
        shutil.copyfile(REPO/'scripts/menu.sh',self.menu_dir/'menu.sh')
        manifest=self.base/'repository/main/deploy/SHA256SUMS';manifest.parent.mkdir(parents=True,exist_ok=True)
        manifest.write_text(hashlib.sha256((self.menu_dir/'menu.sh').read_bytes()).hexdigest()+'  menu.sh\n')
        self.manifest()
    def manifest(self):
        (self.assets/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in sorted(self.assets.glob('shell-wol-*'))))
    def run_install(self,*args,service=False,success=True):
        # Source-only mode permits a fixture service manager without system writes.
        prefix='. "$1"; shift; '
        if service:
            prefix+='choose_service() { SERVICE=systemd; SERVICE_FILE="$FIXTURE_SERVICE/shell-wol.service"; SERVICE_USER=; SERVICE_GROUP=fixture; LAUNCH_DOMAIN=; }; prepare_service_user() { SERVICE_GROUP=fixture; }; '
        result=subprocess.run(['sh','-c',prefix+'main "$@"','installer',str(INSTALLER),'--user','--arch','amd64','--install-dir',str(self.root),'--yes',*args],env=self.env,text=True,capture_output=True)
        if success:self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        else:self.assertNotEqual(result.returncode,0,result.stdout+result.stderr)
        return result
    def ready(self):
        p=self.root/'data/config.yaml';p.write_text('password_hash: ready\nlog_limit: 0\n# preserve\n')
    def test_first_install_and_repeat_upgrade(self):
        self.run_install(); self.assertTrue((self.root/'bin/shell-wol').exists())
        self.ready();before=(self.root/'data/config.yaml').read_bytes()
        self.make_release('v1.2.4');self.run_install('--version','v1.2.4')
        self.assertEqual((self.root/'data/config.yaml').read_bytes(),before)
        self.assertIn('v1.2.3',(self.root/'backup/shell-wol.previous').read_text())
        self.run_install();self.assertFalse((self.root/'.install-lock').exists())
    def test_corrupt_download_cannot_replace_binary(self):
        self.run_install();before=(self.root/'bin/shell-wol').read_bytes()
        (self.assets/'shell-wol-linux-amd64').write_text('corrupt')
        self.run_install(success=False);self.assertEqual((self.root/'bin/shell-wol').read_bytes(),before)
    def test_missing_download_and_bad_version(self):
        (self.assets/'VERSION').write_text('../../bad')
        self.run_install(success=False);self.assertFalse(self.root.exists())
        self.make_release('v1.2.3');(self.assets/'shell-wol-linux-amd64').unlink()
        self.run_install(success=False);self.assertFalse(self.root.exists())
    def test_unmanaged_directory_and_symlink_refused(self):
        self.root.mkdir();(self.root/'keep').write_text('user file')
        self.run_install(success=False);self.assertEqual((self.root/'keep').read_text(),'user file')
        shutil.rmtree(self.root);self.root.symlink_to(self.assets,target_is_directory=True)
        self.run_install(success=False)
    def test_runtime_lock_not_deleted(self):
        self.run_install();(self.root/'data/.lock').mkdir()
        before=(self.root/'bin/shell-wol').read_bytes()
        self.make_release('v1.2.4');self.run_install(success=False)
        self.assertTrue((self.root/'data/.lock').exists());self.assertEqual((self.root/'bin/shell-wol').read_bytes(),before)
    def test_service_starts_before_password_setup(self):
        self.run_install('--service',service=True)
        self.assertTrue((self.service/'shell-wol.service').exists());self.assertTrue((self.service/'active').exists())
        self.ready();self.run_install(service=True)
        self.assertTrue((self.service/'active').exists());self.assertTrue((self.service/'enabled').exists())
        text=(self.service/'shell-wol.service').read_text();self.assertIn(str(self.root),text);self.assertNotIn('@ROOT@',text)
    @unittest.skipUnless(shutil.which('systemd-analyze'), 'systemd-analyze is required for unit verification')
    def test_systemd_unit_parser(self):
        # Parse real generated units, including a root containing spaces. Shell
        # syntax and mocked systemctl cannot validate systemd directive syntax.
        for name in ('plain-root', 'Root With Spaces'):
            with self.subTest(root=name):
                root=self.base/name
                (root/'bin').mkdir(parents=True)
                (root/'data').mkdir()
                shutil.copyfile(self.assets/'shell-wol-linux-amd64',root/'bin/shell-wol')
                (root/'bin/shell-wol').chmod(0o755)
                command='. "$1"; TMP="$2"; ROOT="$3"; SERVICE=systemd; SERVICE_GROUP=fixture; render_service'
                subprocess.run(['sh','-c',command,'test',str(INSTALLER),str(self.template_dir),str(root)],env=self.env,check=True)
                unit=self.service/'shell-wol.service'
                shutil.copyfile(self.template_dir/'rendered-service',unit)
                runtime=self.base/'runtime';runtime.mkdir(exist_ok=True);runtime.chmod(0o700)
                env=os.environ.copy();env['XDG_RUNTIME_DIR']=str(runtime)
                result=subprocess.run(['systemd-analyze','--user','verify','--man=no',str(unit)],env=env,text=True,capture_output=True)
                self.assertEqual(result.returncode,0,result.stdout+result.stderr)
    def test_failed_fresh_install_can_retry(self):
        self.env['FAIL_RELOAD']='1'
        self.run_install('--service',service=True,success=False)
        self.assertFalse((self.root/'bin/shell-wol').exists())
        self.assertTrue((self.root/'data/config.yaml').exists())
        self.assertTrue((self.root/'.shell-wol-install').exists())
        del self.env['FAIL_RELOAD']
        self.run_install('--service',service=True)
        self.assertTrue((self.root/'bin/shell-wol').exists())
    def test_modified_service_refused(self):
        self.run_install('--service',service=True)
        p=self.service/'shell-wol.service';p.write_text(p.read_text()+'# custom\n')
        self.run_install(service=True,success=False);self.assertTrue(p.read_text().endswith('# custom\n'))
    def test_failed_start_restores_old_binary_and_service(self):
        self.run_install('--service',service=True);self.ready();self.run_install(service=True)
        old_state=(self.root/'.shell-wol-install').read_bytes();old_service=(self.service/'shell-wol.service').read_bytes()
        self.make_release('v1.2.4');self.env['FAIL_RUNNING_VERSION']='v1.2.4'
        self.run_install(service=True,success=False)
        self.assertEqual((self.root/'.shell-wol-install').read_bytes(),old_state)
        self.assertEqual((self.service/'shell-wol.service').read_bytes(),old_service)
        self.assertIn('v1.2.3',(self.root/'bin/shell-wol').read_text())
        self.assertTrue((self.service/'active').exists());self.assertTrue((self.service/'enabled').exists())
    def test_detection_and_macos_template(self):
        fs=self.base/'rootfs';(fs/'etc').mkdir(parents=True);(fs/'etc/openwrt_release').touch()
        command='. "$1"; detect_firmware "$2"'
        result=subprocess.check_output(['sh','-c',command,'test',str(INSTALLER),str(fs)],env=self.env,text=True)
        self.assertEqual(result.strip(),'openwrt')
        p=fs/'data/etc/crontabs';p.mkdir(parents=True);(p/'root').touch()
        result=subprocess.check_output(['sh','-c',command,'test',str(INSTALLER),str(fs)],env=self.env,text=True)
        self.assertEqual(result.strip(),'xiaomi')
        command='. "$1"; TMP="$2"; ROOT="/Users/test/Library/Application Support/Shell-WoL"; SERVICE=launchd-user; SERVICE_USER=test; render_service'
        subprocess.run(['sh','-c',command,'test',str(INSTALLER),str(self.template_dir)],env=self.env,check=True)
        import plistlib
        plist=plistlib.loads((self.template_dir/'rendered-service').read_bytes());self.assertEqual(plist['ProgramArguments'][0],'/Users/test/Library/Application Support/Shell-WoL/bin/shell-wol')
    def test_invalid_yaml_preserved(self):
        self.run_install();self.ready()
        (self.root/'data/invalid').touch()
        before=(self.root/'bin/shell-wol').read_bytes()
        self.make_release('v1.2.4');self.run_install(success=False)
        self.assertEqual((self.root/'bin/shell-wol').read_bytes(),before)
        self.assertTrue((self.root/'data/invalid').exists())
    def test_procd_template_with_spaces(self):
        command='. "$1"; TMP="$2"; ROOT="/data/Install With Spaces"; SERVICE=procd; render_service'
        subprocess.run(['sh','-c',command,'test',str(INSTALLER),str(self.template_dir)],env=self.env,check=True)
        text=(self.template_dir/'rendered-service').read_text()
        self.assertIn("INSTALL_ROOT='/data/Install With Spaces'",text)
        self.assertNotIn('@ROOT@',text)
        subprocess.run(['sh','-n',str(self.template_dir/'rendered-service')],check=True)
    def test_template_downloads_pinned_to_binary_tag(self):
        self.make_release('v1.2.4')
        self.run_install('--service','--version','v1.2.3',service=True)
        requests=(self.base/'downloads').read_text()
        self.assertIn('/download/v1.2.3/shell-wol-linux-amd64',requests)
        self.assertIn('/Shell-WoL/v1.2.3/deploy/SHA256SUMS',requests)
        self.assertIn('/Shell-WoL/v1.2.3/deploy/shell-wol.service',requests)
        self.assertNotIn('/main/deploy/shell-wol.',requests)
        self.assertIn('/main/scripts/menu.sh',requests)
        self.assertNotIn('/v1.2.4/',requests)
    def test_template_corruption_preserves_existing_install(self):
        self.run_install('--service',service=True)
        binary=(self.root/'bin/shell-wol').read_bytes()
        unit=(self.service/'shell-wol.service').read_bytes()
        self.make_release('v1.2.4')
        p=self.template_dir/'shell-wol.service';p.write_text(p.read_text()+'# corrupt\n')
        self.run_install(service=True,success=False)
        self.assertEqual((self.root/'bin/shell-wol').read_bytes(),binary)
        self.assertEqual((self.service/'shell-wol.service').read_bytes(),unit)
    def test_missing_tag_manifest_has_no_main_fallback(self):
        (self.template_dir/'SHA256SUMS').unlink()
        result=self.run_install('--service',service=True,success=False)
        self.assertIn('不会回退到 main',result.stderr)
        self.assertFalse(self.root.exists())
        self.assertNotIn('/main/',(self.base/'downloads').read_text())
    def test_missing_tag_template_has_no_release_fallback(self):
        (self.template_dir/'shell-wol.service').unlink()
        self.run_install('--service',service=True,success=False)
        self.assertNotIn('/download/v1.2.3/shell-wol.service',(self.base/'downloads').read_text())
    def test_separate_mirror_addresses(self):
        self.env['FIXTURE_RELEASE_BASE']='https://release.example/releases'
        self.env['FIXTURE_REPO_BASE']='https://repo.example/raw/lemoeo/Shell-WoL'
        self.run_install('--service','--download-base',self.env['FIXTURE_RELEASE_BASE']+'/',
                         '--repo-base',self.env['FIXTURE_REPO_BASE']+'/',service=True)
        requests=(self.base/'downloads').read_text()
        self.assertIn('https://release.example/releases/download/v1.2.3/',requests)
        self.assertIn('https://repo.example/raw/lemoeo/Shell-WoL/v1.2.3/deploy/',requests)
    def test_old_release_extra_assets_are_ignored(self):
        p=self.assets/'SHA256SUMS';p.write_text(p.read_text()+'0'*64+'  shell-wol.service\n')
        self.run_install('--service',service=True)
        self.assertNotIn('/download/v1.2.3/shell-wol.service',(self.base/'downloads').read_text())
    def test_template_checksum_helper_detects_stale_manifest(self):
        project=self.base/'project';(project/'scripts').mkdir(parents=True);(project/'deploy').mkdir()
        shutil.copyfile(REPO/'scripts/menu.sh',project/'scripts/menu.sh')
        helper=project/'scripts/template-checksums.sh';shutil.copyfile(REPO/'scripts/template-checksums.sh',helper)
        for name in ('shell-wol.service','shell-wol.openwrt','shell-wol.plist','SHA256SUMS'):
            shutil.copyfile(REPO/'deploy'/name,project/'deploy'/name)
        subprocess.run(['sh',str(helper),'--check'],check=True)
        p=project/'deploy/shell-wol.service';p.write_text(p.read_text()+'# changed\n')
        result=subprocess.run(['sh',str(helper),'--check'],capture_output=True)
        self.assertNotEqual(result.returncode,0)
        subprocess.run(['sh',str(helper)],check=True)
        subprocess.run(['sh',str(helper),'--check'],check=True)
    def test_invalid_paths_and_arch(self):
        for argument in ('unknown','386'):
            self.run_install('--arch',argument,success=False)
        self.run_install('--install-dir','/tmp/shell-wol',success=False)
        self.run_install('--install-dir',str(self.root)+'\n/evil',success=False)
        self.run_install('--version','v1.2.3\nv1.2.4',success=False)
        self.run_install('--repo-base','http://insecure.example',success=False)

if __name__=='__main__':unittest.main(verbosity=2)
