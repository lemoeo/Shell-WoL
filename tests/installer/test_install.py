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
import os, pathlib, shutil, sys
args=sys.argv[1:]
assert '--proto' in args and args[args.index('--proto')+1]=='=https'
assert '--proto-redir' in args and args[args.index('--proto-redir')+1]=='=https'
url=args[-1]; assert url.startswith('https://')
source=pathlib.Path(os.environ['FIXTURE_ASSETS']) / url.rsplit('/',1)[-1]
if not source.exists():sys.exit(22)
shutil.copyfile(source,args[args.index('--output')+1])
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
        self.assets = self.base/'assets'; self.assets.mkdir()
        self.tools = self.base/'tools'; self.tools.mkdir()
        self.root = self.base/'Install With Spaces'
        self.service = self.base/'service'; self.service.mkdir()
        self.env = os.environ.copy()
        self.env.update(PATH=str(self.tools)+os.pathsep+self.env['PATH'], FIXTURE_ASSETS=str(self.assets), FIXTURE_SERVICE=str(self.service), SHELL_WOL_INSTALL_LIBRARY='1')
        self.tool('curl', CURL)
        self.tool('systemctl', SYSTEMCTL)
        self.tool('sleep', '#!/bin/sh\nexit 0\n')
        self.make_release('v1.2.3')

    def tearDown(self): self.temp.cleanup()
    def tool(self,name,text):
        p=self.tools/name; p.write_text(text);p.chmod(0o755)
    def make_release(self,version):
        (self.assets/'VERSION').write_text(version+'\n')
        p=self.assets/'shell-wol-linux-amd64';p.write_text(BINARY.replace('VERSION_VALUE',version));p.chmod(0o755)
        for name in ('shell-wol.service','shell-wol.openwrt','shell-wol.plist'):
            shutil.copyfile(REPO/'deploy'/name,self.assets/name)
        self.manifest()
    def manifest(self):
        (self.assets/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in sorted(self.assets.iterdir()) if p.name!='SHA256SUMS'))
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
    def test_service_registration_defers_until_password(self):
        self.run_install('--service',service=True)
        self.assertTrue((self.service/'shell-wol.service').exists());self.assertFalse((self.service/'active').exists())
        self.ready();self.run_install(service=True)
        self.assertTrue((self.service/'active').exists());self.assertTrue((self.service/'enabled').exists())
        text=(self.service/'shell-wol.service').read_text();self.assertIn(str(self.root),text);self.assertNotIn('@ROOT@',text)
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
        subprocess.run(['sh','-c',command,'test',str(INSTALLER),str(self.assets)],env=self.env,check=True)
        import plistlib
        plist=plistlib.loads((self.assets/'rendered-service').read_bytes());self.assertEqual(plist['ProgramArguments'][0],'/Users/test/Library/Application Support/Shell-WoL/bin/shell-wol')
    def test_invalid_yaml_preserved(self):
        self.run_install();self.ready()
        (self.root/'data/invalid').touch()
        before=(self.root/'bin/shell-wol').read_bytes()
        self.make_release('v1.2.4');self.run_install(success=False)
        self.assertEqual((self.root/'bin/shell-wol').read_bytes(),before)
        self.assertTrue((self.root/'data/invalid').exists())
    def test_procd_template_with_spaces(self):
        command='. "$1"; TMP="$2"; ROOT="/data/Install With Spaces"; SERVICE=procd; render_service'
        subprocess.run(['sh','-c',command,'test',str(INSTALLER),str(self.assets)],env=self.env,check=True)
        text=(self.assets/'rendered-service').read_text()
        self.assertIn("INSTALL_ROOT='/data/Install With Spaces'",text)
        self.assertNotIn('@ROOT@',text)
        subprocess.run(['sh','-n',str(self.assets/'rendered-service')],check=True)
    def test_invalid_paths_and_arch(self):
        for argument in ('unknown','386'):
            self.run_install('--arch',argument,success=False)
        self.run_install('--install-dir','/tmp/shell-wol',success=False)
        self.run_install('--install-dir',str(self.root)+'\n/evil',success=False)
        self.run_install('--version','v1.2.3\nv1.2.4',success=False)

if __name__=='__main__':unittest.main(verbosity=2)
