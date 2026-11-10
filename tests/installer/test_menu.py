"""Small menu/installer integration. All service commands use private fixtures."""
import hashlib
import os
import pty
import select
import subprocess
import unittest

import test_install as fixture


class MenuTests(unittest.TestCase):
    def setUp(self):
        self.f=fixture.InstallerTests();self.f.setUp()
        self.root=self.f.root
    def tearDown(self):self.f.tearDown()
    def menu(self,*args,code=0):
        r=subprocess.run(['sh',str(self.root/'menu.sh'),*args],env=self.f.env,
                         text=True,capture_output=True,timeout=10)
        self.assertEqual(r.returncode,code,r.stdout+r.stderr)
        return r
    def control(self,action,code=0):
        # Exercise real dispatch/ownership/locking with a fixture service and PID.
        env=dict(self.f.env,SHELL_WOL_MENU_LIBRARY='1')
        prefix=r'''. "$1"; shift
setup() {
 ROOT=$1;BIN=$ROOT/bin/shell-wol;DATA=$ROOT/data
 SERVICE=systemd;UNIT=$FIXTURE_SERVICE/shell-wol.service
}
find_pid() { [ -f "$FIXTURE_SERVICE/active" ] && echo "$$"; }
setup "$1";shift
control "$1"
'''
        r=subprocess.run(['sh','-c',prefix,'test',str(fixture.REPO/'scripts/menu.sh'),
                          str(self.root),action],env=env,text=True,capture_output=True,timeout=10)
        self.assertEqual(r.returncode,code,r.stdout+r.stderr)
        return r

    def test_single_menu_alias_and_schema_stay_simple(self):
        self.f.run_install();self.f.run_install()
        state=(self.root/'.shell-wol-install').read_text()
        self.assertIn('schema=1',state)
        self.assertNotIn('manager_',state)
        self.assertEqual((self.f.home/'.profile').read_text().count('alias swol='),1)
        r=subprocess.run(['sh','-c','. "$1"\nswol status','test',str(self.f.home/'.profile')],
                         env=self.f.env,text=True,capture_output=True)
        self.assertEqual(r.returncode,1,r.stdout+r.stderr)
        self.assertIn('运行状态：已停止',r.stdout)
        self.assertFalse((self.root/'control.sh').exists())
        self.assertFalse((self.root/'lib').exists())

    def test_bad_menu_checksum_preserves_program_and_profile(self):
        self.f.run_install()
        before={n:(self.root/n).read_bytes() for n in ('menu.sh','bin/shell-wol','.shell-wol-install')}
        profile=(self.f.home/'.profile').read_bytes()
        self.f.env['CORRUPT_CDN_SUFFIX']='/scripts/menu.sh'
        self.f.run_install('--source','jsdelivr',success=False)
        for n,data in before.items():self.assertEqual((self.root/n).read_bytes(),data)
        self.assertEqual((self.f.home/'.profile').read_bytes(),profile)

    def test_failed_metadata_write_restores_menu_profile_and_program(self):
        self.f.run_install()
        profile=self.f.home/'.profile'
        profile.write_text('# user setting\n'+profile.read_text())
        before={n:(self.root/n).read_bytes() for n in ('menu.sh','bin/shell-wol','.shell-wol-install')}
        old_profile=profile.read_bytes()
        self.f.make_release('v1.2.4')
        p=self.f.menu_dir/'menu.sh';p.write_text(p.read_text()+'# newer menu\n')
        (self.f.base/'repository/main/deploy/SHA256SUMS').write_text(hashlib.sha256(p.read_bytes()).hexdigest()+'  menu.sh\n')
        self.f.tool('mv',r'''#!/bin/sh
for arg in "$@"; do
 case "$arg" in */.shell-wol-install)
  if [ ! -e "$FIXTURE_ROOT/failed-once" ]; then touch "$FIXTURE_ROOT/failed-once";exit 1;fi;;
 esac
done
exec /bin/mv "$@"
''')
        self.f.run_install(success=False)
        for n,data in before.items():self.assertEqual((self.root/n).read_bytes(),data)
        self.assertEqual(profile.read_bytes(),old_profile)
        self.assertFalse((self.root/'.install-lock').exists())

    def test_service_dispatch_and_stop_keeps_autostart(self):
        self.f.run_install('--service',service=True)
        self.f.tool('id','#!/bin/sh\necho 0\n')
        self.control('stop')
        self.assertFalse((self.f.service/'active').exists())
        self.assertTrue((self.f.service/'enabled').exists())
        self.control('start');self.assertTrue((self.f.service/'active').exists())
        self.control('restart');self.assertTrue((self.f.service/'active').exists())

    def test_lock_and_foreign_unit_are_preserved(self):
        self.f.run_install('--service',service=True)
        self.f.tool('id','#!/bin/sh\necho 0\n')
        (self.root/'.install-lock').mkdir()
        self.control('stop',code=1)
        self.assertTrue((self.root/'.install-lock').exists())
        (self.root/'.install-lock').rmdir()
        unit=self.f.service/'shell-wol.service'
        unit.write_text(unit.read_text().replace(str(self.root),'/opt/other'))
        self.control('stop',code=1)
        self.assertTrue((self.f.service/'active').exists())

    def test_no_service_and_metadata_are_not_executed(self):
        self.f.run_install()
        marker=self.f.base/'executed'
        with (self.root/'.shell-wol-install').open('a') as f:f.write('other=$(touch '+str(marker)+')\n')
        self.menu('status',code=1);self.assertFalse(marker.exists())
        self.menu('start',code=1)
        self.menu('invalid',code=2)

    def test_menu_requires_terminal_and_opens_in_pty(self):
        self.f.run_install()
        self.menu(code=2)
        master,slave=pty.openpty()
        try:
            p=subprocess.Popen(['sh',str(self.root/'menu.sh')],env=self.f.env,
                               stdin=slave,stdout=slave,stderr=slave)
            os.write(master,b'0\n');p.wait(timeout=5)
            output=b''
            while select.select([master],[],[],.1)[0]:output+=os.read(master,65536)
            self.assertEqual(p.returncode,0,output.decode())
            self.assertIn('内存（RSS）',output.decode())
        finally:os.close(master);os.close(slave)


if __name__=='__main__':unittest.main()
