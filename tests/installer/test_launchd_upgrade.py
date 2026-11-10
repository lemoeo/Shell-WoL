"""launchd upgrade states using isolated files and a strict launchctl fixture."""
import shutil
import subprocess
import unittest

import test_install as fixture

LAUNCHCTL = r'''#!/bin/sh
folder=$FIXTURE_SERVICE
case "$1" in
print)
 case "$2" in */io.github.lemoeo.shell-wol) [ -f "$folder/active" ] ;; *) : ;; esac ;;
print-disabled)
 if [ -f "$folder/enabled" ]; then echo '{}'
 else echo '{ "io.github.lemoeo.shell-wol" => true }'; fi ;;
enable) touch "$folder/enabled" ;;
disable) rm -f "$folder/enabled" ;;
bootout) rm -f "$folder/active" ;;
bootstrap)
 [ -f "$folder/enabled" ] || exit 5
 [ "${BOOTSTRAP_FAIL_ONCE:-0}" != 1 ] || [ -e "$folder/failed" ] || { touch "$folder/failed"; exit 6; }
 touch "$folder/active" ;;
*) exit 2 ;;
esac
'''


class LaunchdUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.f=fixture.InstallerTests();self.f.setUp()
        self.f.tool('uname','#!/bin/sh\ncase "$1" in -s) echo Darwin;; -m) echo x86_64;; *) exit 2;; esac\n')
        self.f.tool('launchctl',LAUNCHCTL)
        shutil.copyfile(self.f.assets/'shell-wol-linux-amd64',self.f.assets/'shell-wol-darwin-amd64')
        self.f.manifest()
    def tearDown(self):self.f.tearDown()
    def install(self,success=True):
        command=r'''. "$1"; shift
choose_service() {
 SERVICE=launchd-user;SERVICE_FILE=$FIXTURE_SERVICE/shell-wol.plist
 SERVICE_USER=;SERVICE_GROUP=;LAUNCH_DOMAIN=gui/1000
}
prepare_service_user() { :; }
main "$@"
'''
        r=subprocess.run(['sh','-c',command,'installer',str(fixture.INSTALLER),
            '--user','--install-dir',str(self.f.root),'--service','--yes'],
            env=self.f.env,text=True,capture_output=True)
        self.assertEqual(r.returncode==0,success,r.stdout+r.stderr)
    def states(self,running,enabled):
        for name,present in (('active',running),('enabled',enabled)):
            p=self.f.service/name
            if present:p.touch()
            else:p.unlink(missing_ok=True)
    def test_upgrade_keeps_running_and_autostart_independent(self):
        self.install()
        for running,enabled in ((False,False),(False,True),(True,False),(True,True)):
            with self.subTest(running=running,enabled=enabled):
                self.states(running,enabled);self.install()
                self.assertEqual((self.f.service/'active').exists(),running)
                self.assertEqual((self.f.service/'enabled').exists(),enabled)
    def test_failed_disabled_running_upgrade_restores_old_job(self):
        self.install();self.states(True,False)
        before=(self.f.root/'.shell-wol-install').read_bytes()
        self.f.env['BOOTSTRAP_FAIL_ONCE']='1'
        self.install(success=False)
        self.assertEqual((self.f.root/'.shell-wol-install').read_bytes(),before)
        self.assertTrue((self.f.service/'active').exists())
        self.assertFalse((self.f.service/'enabled').exists())

if __name__=='__main__':unittest.main()
