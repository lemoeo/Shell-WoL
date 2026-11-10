"""Offline uninstall lifecycle tests (Python + Go); managers use private fixtures."""
import hashlib
import os
from pathlib import Path
import shutil
import select
import subprocess
import unittest

import test_install as install_fixture

REPO = install_fixture.REPO

UNINSTALLER = REPO/'scripts/uninstall.sh'
SYSTEMCTL = r'''#!/bin/sh
folder=$FIXTURE_SERVICE
printf '%s\n' "$*" >> "$folder/calls"
case "$1" in
is-active)
 [ "${STATUS_ERROR:-0}" != 1 ] || exit 1
 if [ -f "$folder/active" ]; then [ "${2:-}" = --quiet ] || echo active; exit 0
 else [ "${2:-}" = --quiet ] || echo inactive; exit 3; fi;;
is-enabled) [ -f "$folder/enabled" ];;
start) touch "$folder/active";;
stop)
 [ "${STOP_FAIL:-0}" != 1 ] || exit 1
 [ "${STICKY_STOP:-0}" = 1 ] || rm -f "$folder/active";;
enable) touch "$folder/enabled";;
disable) [ "${DISABLE_FAIL:-0}" != 1 ] || exit 1; rm -f "$folder/enabled";;
daemon-reload) [ "${FAIL_RELOAD:-0}" != 1 ];;
*) exit 2;;
esac
'''
LAUNCHCTL = r'''#!/bin/sh
folder=$FIXTURE_SERVICE
printf '%s\n' "$*" >> "$folder/calls"
case "$1" in
print)
 [ "${LAUNCH_STATUS_ERROR:-0}" != 1 ] || exit 1
 case "$2" in */io.github.lemoeo.shell-wol) [ -f "$folder/active" ] || exit 113;; *) :;; esac;;
bootout) [ "${STOP_FAIL:-0}" != 1 ] || exit 1; rm -f "$folder/active";;
disable) rm -f "$folder/enabled";;
*) exit 2;;
esac
'''

class UninstallTests(unittest.TestCase):
    def setUp(self):
        self.fixture=install_fixture.InstallerTests();self.fixture.setUp()
        self.fixture.tool('systemctl',SYSTEMCTL)
        self.fixture.tool('launchctl',LAUNCHCTL)
        self.env=self.fixture.env.copy();self.env['SHELL_WOL_UNINSTALL_LIBRARY']='1'
        self.root=self.fixture.root;self.service=self.fixture.service
    def tearDown(self):self.fixture.tearDown()
    def install(self,service=False):self.fixture.run_install(*(['--service'] if service else []),service=service)
    def uninstall(self,*args,success=True):
        # Redirect only service paths/privilege checks; real manager commands are
        # fixture executables. Production code has no service-path override flag.
        prefix='. "$1"; UNINSTALL_SOURCE=$1; shift; resolve_service() { SERVICE_FILE=; LAUNCH_DOMAIN=; if [ "$SERVICE" != none ]; then SERVICE_FILE="$FIXTURE_SERVICE/shell-wol.service"; LAUNCH_DOMAIN=gui/fixture; fi; }; require_service_permissions() { :; }; '
        r=subprocess.run(['sh','-c',prefix+'main "$@"','uninstall',str(UNINSTALLER),'--user','--install-dir',str(self.root),'--yes',*args],env=self.env,text=True,capture_output=True)
        if success:self.assertEqual(r.returncode,0,r.stdout+r.stderr)
        else:self.assertNotEqual(r.returncode,0,r.stdout+r.stderr)
        return r
    def state(self,service,unit=None):
        digest=hashlib.sha256(unit.read_bytes()).hexdigest() if unit else ''
        state=self.root/'.shell-wol-install'
        fields=dict(line.split('=',1) for line in state.read_text().splitlines())
        fields.update(service=service,service_sha256=digest)
        state.write_text(''.join(k+'='+v+'\n' for k,v in fields.items()))
    def test_default_keeps_data_and_reinstall(self):
        self.install(service=True)
        data=self.root/'data';(data/'devices.yaml').write_text('devices: []\n# preserve\n')
        (data/'credentials.yaml').write_text('version: 1\ncredentials: {}\n')
        (data/'credentials.key').write_bytes(b'K'*32)
        before={p.name:p.read_bytes() for p in data.iterdir()}
        self.uninstall()
        self.assertFalse((self.root/'bin/shell-wol').exists())
        self.assertFalse((self.service/'shell-wol.service').exists())
        self.assertFalse((self.service/'active').exists());self.assertFalse((self.service/'enabled').exists())
        self.assertEqual({p.name:p.read_bytes() for p in data.iterdir()},before)
        self.assertIn('status=uninstalled',(self.root/'.shell-wol-install').read_text())
        self.uninstall() # repeated removal is safe
        self.fixture.run_install('--service',service=True)
        self.assertEqual({p.name:p.read_bytes() for p in data.iterdir()},before)
        self.assertTrue((self.service/'active').exists())
    def test_purge_removes_known_files_and_root(self):
        self.install(service=True)
        for name in ('events.yaml','credentials.yaml','credentials.key','service.log','service.err.log'):(self.root/'data'/name).write_text('test\n')
        (self.root/'backup').mkdir(exist_ok=True);(self.root/'backup/shell-wol.previous').write_text('old')
        self.uninstall('--purge');self.assertFalse(self.root.exists())
        self.uninstall('--purge')
    def test_menu_and_only_owned_alias_are_removed(self):
        self.install()
        profile=self.fixture.home/'.profile'
        profile.write_text('# user settings\n'+profile.read_text()+'alias other="echo kept"\n')
        self.uninstall()
        self.assertFalse((self.root/'menu.sh').exists())
        self.assertEqual(profile.read_text(),'# user settings\nalias other="echo kept"\n')
        before=profile.read_bytes()
        self.uninstall()
        self.assertEqual(profile.read_bytes(),before)
    def test_uninstall_removes_owned_legacy_alias_and_preserves_foreign(self):
        self.install()
        profile=self.fixture.home/'.profile'
        legacy=profile.read_text().replace('alias wol=', 'alias swol=')
        foreign='alias wol="echo user command"\n'
        profile.write_text('# user settings\n'+legacy+foreign)
        self.uninstall()
        self.assertEqual(profile.read_text(),'# user settings\n'+foreign)

    def test_purge_preserves_unknown_files_and_empty_folders(self):
        self.install();(self.root/'notes.txt').write_text('user file')
        (self.root/'data/custom.yaml').write_text('custom: yes')
        (self.root/'data/empty').mkdir()
        result=self.uninstall('--purge')
        self.assertEqual((self.root/'notes.txt').read_text(),'user file')
        self.assertTrue((self.root/'data/custom.yaml').exists());self.assertTrue((self.root/'data/empty').exists())
        self.assertFalse((self.root/'data/config.yaml').exists())
        self.assertIn('custom.yaml',result.stdout)
    def test_native_uninstall_preserves_unrelated_boot_names(self):
        self.install()
        outside=self.fixture.base/'outside';outside.write_text('user script')
        (self.root/'boot.sh').symlink_to(outside)
        (self.root/'service').write_text('user file')
        self.uninstall('--purge')
        self.assertTrue((self.root/'boot.sh').is_symlink())
        self.assertEqual((self.root/'service').read_text(),'user file')
        self.assertEqual(outside.read_text(),'user script')
    def test_dry_run_has_no_side_effects(self):
        self.install(service=True)
        before={str(p.relative_to(self.root)):p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        calls=(self.service/'calls').read_bytes()
        self.uninstall('--purge','--dry-run')
        self.assertEqual({str(p.relative_to(self.root)):p.read_bytes() for p in self.root.rglob('*') if p.is_file()},before)
        self.assertEqual((self.service/'calls').read_bytes(),calls)
        self.assertTrue((self.service/'active').exists());self.assertFalse((self.root/'.install-lock').exists())
    def test_modified_service_is_uninstalled_when_path_still_matches(self):
        self.install(service=True);unit=self.service/'shell-wol.service'
        unit.write_text(unit.read_text().replace('User=shell-wol','User=root')+'# custom\n')
        metadata=(self.root/'.shell-wol-install').read_bytes();content=unit.read_bytes()
        self.uninstall('--dry-run')
        self.assertEqual((self.root/'.shell-wol-install').read_bytes(),metadata)
        self.assertEqual(unit.read_bytes(),content)
        self.assertTrue((self.service/'active').exists())
        self.uninstall()
        self.assertFalse(unit.exists());self.assertFalse((self.service/'active').exists())
        self.assertFalse((self.root/'bin/shell-wol').exists());self.assertTrue((self.root/'data/config.yaml').exists())
    def test_working_directory_edit_does_not_block_uninstall(self):
        self.install(service=True);unit=self.service/'shell-wol.service'
        unit.write_text(unit.read_text().replace('WorkingDirectory='+str(self.root),'WorkingDirectory="'+str(self.root)+'"'))
        self.uninstall();self.assertFalse(unit.exists())
    def test_service_digest_is_not_required(self):
        for value in (None, 'old-digest', '0'*64):
            with self.subTest(digest=value):
                self.install(service=True)
                state=self.root/'.shell-wol-install'
                text='\n'.join(line for line in state.read_text().splitlines() if not line.startswith('service_sha256='))+'\n'
                if value is not None:text+='service_sha256='+value+'\n'
                state.write_text(text)
                self.uninstall();self.assertFalse((self.root/'bin/shell-wol').exists())
    def test_foreign_root_service_is_rejected_without_digest_check(self):
        self.install(service=True);unit=self.service/'shell-wol.service';unit.write_text(unit.read_text().replace(str(self.root),'/opt/other-install'))
        self.state('systemd',unit)
        self.uninstall(success=False);self.assertTrue((self.service/'active').exists())
    def test_stop_failure_keeps_program_and_data(self):
        self.install(service=True);self.env['STOP_FAIL']='1';self.uninstall('--purge',success=False)
        self.assertTrue((self.root/'bin/shell-wol').exists());self.assertTrue((self.root/'data/config.yaml').exists())
        self.assertTrue((self.service/'shell-wol.service').exists());self.assertFalse((self.root/'.install-lock').exists())
    def test_service_still_running_is_rejected(self):
        self.install(service=True);self.env['STICKY_STOP']='1';self.uninstall(success=False)
        self.assertTrue((self.root/'bin/shell-wol').exists())
    def test_unknown_service_state_is_rejected(self):
        self.install(service=True);self.env['STATUS_ERROR']='1';self.uninstall(success=False)
        self.assertTrue((self.root/'bin/shell-wol').exists());self.assertTrue((self.service/'active').exists())
    def test_disable_failure_can_retry(self):
        self.install(service=True);self.env['DISABLE_FAIL']='1';self.uninstall(success=False)
        self.assertTrue((self.service/'shell-wol.service').exists());self.assertTrue((self.root/'bin/shell-wol').exists())
        del self.env['DISABLE_FAIL'];self.uninstall()
    def test_reload_failure_can_retry_with_missing_unit(self):
        self.install(service=True);self.env['FAIL_RELOAD']='1';self.uninstall(success=False)
        self.assertFalse((self.service/'shell-wol.service').exists());self.assertTrue((self.root/'bin/shell-wol').exists())
        del self.env['FAIL_RELOAD'];self.uninstall()
    def test_inactive_missing_service_is_tolerated(self):
        self.install(service=True);(self.service/'active').unlink();(self.service/'shell-wol.service').unlink()
        self.uninstall();self.assertFalse((self.root/'bin/shell-wol').exists())
    def test_missing_systemd_unit_removes_only_its_enable_links(self):
        self.install(service=True);(self.service/'active').unlink();(self.service/'shell-wol.service').unlink()
        wants=self.service/'multi-user.target.wants';wants.mkdir()
        owned=wants/'shell-wol.service';owned.symlink_to('../shell-wol.service')
        requires=self.service/'other.target.requires';requires.mkdir()
        foreign=requires/'shell-wol.service';foreign.symlink_to('/opt/another/unit.service')
        self.uninstall()
        self.assertFalse(owned.is_symlink());self.assertTrue(foreign.is_symlink())
    def test_missing_procd_unit_status_is_unknown(self):
        self.install();self.state('procd')
        self.uninstall(success=False);self.assertTrue((self.root/'bin/shell-wol').exists())
    def test_active_missing_service_is_not_stopped(self):
        self.install(service=True);(self.service/'shell-wol.service').unlink()
        self.uninstall(success=False);self.assertTrue((self.service/'active').exists())
    def test_runtime_lock_is_never_deleted(self):
        self.install();(self.root/'data/.lock').mkdir()
        self.uninstall('--purge',success=False)
        self.assertTrue((self.root/'data/.lock').exists());self.assertTrue((self.root/'bin/shell-wol').exists())
    def test_install_lock_is_never_deleted(self):
        self.install();(self.root/'.install-lock').mkdir()
        self.uninstall(success=False);self.assertTrue((self.root/'.install-lock').exists())
    def test_symlink_and_unknown_roots_are_rejected(self):
        self.root.mkdir();(self.root/'keep').write_text('keep')
        self.uninstall('--purge',success=False);self.assertEqual((self.root/'keep').read_text(),'keep')
        shutil.rmtree(self.root);self.install()
        original=self.root/'bin/shell-wol';original.unlink();original.symlink_to(self.fixture.assets/'shell-wol-linux-amd64')
        self.uninstall(success=False);self.assertTrue(original.is_symlink())
    def test_data_symlink_not_followed_for_purge(self):
        self.install();outside=self.fixture.base/'outside';outside.write_text('outside')
        p=self.root/'data/config.yaml';p.unlink();p.symlink_to(outside)
        self.uninstall('--purge',success=False);self.assertEqual(outside.read_text(),'outside')
    def test_credential_symlinks_are_not_followed_for_purge(self):
        for name in ('credentials.yaml', 'credentials.key'):
            with self.subTest(name=name):
                self.install()
                outside=self.fixture.base/('outside-'+name);outside.write_text('private outside fixture')
                link=self.root/'data'/name;link.symlink_to(outside)
                self.uninstall('--purge',success=False)
                self.assertEqual(outside.read_text(),'private outside fixture')
                self.assertTrue(link.is_symlink())
                link.unlink();self.uninstall('--purge')
    def test_invalid_root_paths_are_rejected(self):
        for root in ('/', '///', '/opt', '/etc', str(self.root)+'/../other'):
            with self.subTest(root=root):self.uninstall('--install-dir',root,success=False)
    def test_root_symlink_is_not_followed(self):
        self.install();actual=self.fixture.base/'actual';self.root.rename(actual)
        self.root.symlink_to(actual,target_is_directory=True)
        self.uninstall('--purge',success=False)
        self.assertTrue((actual/'bin/shell-wol').exists());self.assertTrue((actual/'data/config.yaml').exists())
    def test_metadata_is_not_executed(self):
        self.install();marker=self.fixture.base/'executed'
        with (self.root/'.shell-wol-install').open('a') as state:state.write(f'other=$(touch {marker})\n')
        self.uninstall();self.assertFalse(marker.exists())
    def test_invalid_metadata_version_is_rejected(self):
        self.install();p=self.root/'.shell-wol-install';p.write_text(p.read_text()+'version=service=systemd\n')
        self.uninstall(success=False);self.assertTrue((self.root/'bin/shell-wol').exists())
    @unittest.skipUnless(Path('/proc/self').exists(), 'Linux executable inspection test')
    def test_manual_process_without_data_lock_is_detected(self):
        self.install();binary=self.root/'bin/shell-wol';binary.unlink()
        # A copied host utility may dispatch by its name, use a wrapper, or exit
        # during startup. Build a controlled executable and wait for its explicit
        # readiness message before asking the uninstaller to inspect processes.
        result=subprocess.run(['go','build','-o',str(binary),str(REPO/'tests/installer/testdata/running.go')],
                              env=dict(os.environ,CGO_ENABLED='0'),text=True,capture_output=True)
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        process=subprocess.Popen([str(binary)],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        try:
            ready,_,_=select.select([process.stdout],[],[],5)
            self.assertTrue(ready,'process fixture did not become ready within 5 seconds')
            line=process.stdout.readline()
            self.assertEqual(line,'ready\n','process fixture failed to start: '+line)
            self.assertIsNone(process.poll(),'process fixture exited before inspection')
            self.assertEqual(os.readlink(f'/proc/{process.pid}/exe'),str(binary),
                             'fixture must execute the installed binary, not a wrapper')
            self.uninstall('--purge',success=False)
            self.assertTrue(binary.exists());self.assertIsNone(process.poll())
            self.assertFalse((self.root/'data/.lock').exists())
        finally:
            if process.poll() is None:process.terminate()
            process.wait(timeout=5);process.stdout.close();process.stderr.close()
    def test_unknown_process_inspection_preserves_files(self):
        self.install();self.fixture.tool('readlink','#!/bin/sh\nexit 1\n')
        self.uninstall('--purge',success=False)
        self.assertTrue((self.root/'bin/shell-wol').exists());self.assertTrue((self.root/'data/config.yaml').exists())
        self.assertFalse((self.root/'data/.lock').exists())
    def test_procd_stop_disable_and_removal(self):
        self.install();unit=self.service/'shell-wol.service'
        unit.write_text(f'''#!/bin/sh
INSTALL_ROOT='{self.root}'
case "$1" in
running) [ -f "$FIXTURE_SERVICE/active" ];;
stop) rm -f "$FIXTURE_SERVICE/active";;
disable) rm -f "$FIXTURE_SERVICE/enabled";;
*) exit 2;;
esac
''');unit.chmod(0o755);self.state('procd',unit)
        (self.service/'active').touch();(self.service/'enabled').touch()
        self.uninstall();self.assertFalse(unit.exists());self.assertFalse((self.service/'active').exists())
    def test_launchd_stop_disable_and_removal(self):
        for kind in ('launchd-user','launchd-system'):
            with self.subTest(kind=kind):
                self.install();unit=self.service/'shell-wol.service'
                unit.write_text(f'<plist><string>{self.root}/bin/shell-wol</string><string>{self.root}/data</string></plist>')
                self.state(kind,unit);(self.service/'active').touch();(self.service/'enabled').touch()
                self.uninstall();self.assertFalse(unit.exists());self.assertFalse((self.service/'active').exists())
    def test_launchd_unknown_domain_keeps_files(self):
        self.install();unit=self.service/'shell-wol.service';unit.write_text(f'<string>{self.root}/bin/shell-wol</string><string>{self.root}/data</string>')
        self.state('launchd-user',unit);self.env['LAUNCH_STATUS_ERROR']='1'
        self.uninstall(success=False);self.assertTrue(unit.exists());self.assertTrue((self.root/'bin/shell-wol').exists())

if __name__=='__main__':unittest.main(verbosity=2)
