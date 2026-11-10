"""Menu maintenance with real scripts and isolated transport/service fixtures."""
import errno
import json
import os
import pty
import select
import signal
import subprocess
import time
import unittest

import test_install as fixture


class MenuMaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.f=fixture.InstallerTests();self.f.setUp();self.f.run_install()
        self.root=self.f.root
        self.tmp=self.f.base/'menu-tmp';self.tmp.mkdir()
        self.env=dict(self.f.env,TMPDIR=str(self.tmp))
        self.f.make_release('v1.2.4')
        (self.f.menu_dir/'install.sh').write_text(fixture.INSTALLER.read_text())
    def tearDown(self):self.f.tearDown()
    def terminal(self,command=None,answers='',code=0):
        pid,master=pty.fork()
        if pid==0:
            os.execvpe('sh',['sh',str(self.root/'menu.sh')]+([command] if command else []),self.env)
        output=b'';ended=False
        try:
            if answers:os.write(master,answers.encode())
            deadline=time.monotonic()+25
            while time.monotonic()<deadline:
                if select.select([master],[],[],.1)[0]:
                    try:chunk=os.read(master,65536)
                    except OSError as e:
                        if e.errno==errno.EIO:break
                        raise
                    if not chunk:break
                    output+=chunk
            else:self.fail('menu timed out: '+output.decode(errors='replace'))
            _,status=os.waitpid(pid,0);ended=True
            self.assertEqual(os.waitstatus_to_exitcode(status),code,output.decode(errors='replace'))
        finally:
            os.close(master)
            if not ended:
                try:os.kill(pid,signal.SIGKILL)
                except ProcessLookupError:pass
                os.waitpid(pid,0)
        self.assertEqual(list(self.tmp.iterdir()),[],output.decode(errors='replace'))
        return output.decode(errors='replace')
    def test_github_upgrade_keeps_directory_data_and_exits_old_menu(self):
        data=self.root/'data/config.yaml';before=data.read_bytes()
        output=self.terminal(answers='6\n1\ny\n')
        self.assertIn('version=v1.2.4',(self.root/'.shell-wol-install').read_text())
        self.assertEqual(data.read_bytes(),before)
        self.assertIn('重新输入 wol',output)
        self.assertNotIn('按回车返回',output)
        self.assertEqual(output.count('运行状态：'),1)
        self.assertTrue((self.root/'uninstall.sh').is_file())
        self.assertFalse((self.root/'.install-lock').exists())
    def test_cdn_upgrade_passes_custom_host_to_whole_install(self):
        self.env['FIXTURE_CDN_BASE']='https://testingcf.jsdelivr.net'
        (self.f.base/'downloads').unlink()
        self.terminal('upgrade','2\nhttps://testingcf.jsdelivr.net\ny\n')
        requests=(self.f.base/'downloads').read_text().splitlines()
        self.assertTrue(all(u.startswith('https://testingcf.jsdelivr.net/') for u in requests))
        self.assertIn('@main/scripts/install.sh',requests[0])
    def test_upgrade_cancel_returns_to_menu_without_download(self):
        (self.f.base/'downloads').unlink()
        self.terminal(answers='6\n1\nn\n0\n')
        self.assertFalse((self.f.base/'downloads').exists())
        self.assertIn('version=v1.2.3',(self.root/'.shell-wol-install').read_text())
    def test_download_failure_preserves_installation(self):
        self.env['FAIL_URL_PREFIXES']=json.dumps(['https://raw.githubusercontent.com/'])
        before=(self.root/'.shell-wol-install').read_bytes()
        self.terminal('upgrade','1\ny\n',code=1)
        self.assertEqual((self.root/'.shell-wol-install').read_bytes(),before)
    def test_empty_and_invalid_installers_are_not_executed(self):
        for text in ('','if then broken'):
            with self.subTest(text=text):
                (self.f.menu_dir/'install.sh').write_text(text)
                self.terminal('upgrade','1\ny\n',code=1)
                self.assertIn('version=v1.2.3',(self.root/'.shell-wol-install').read_text())
    def test_upgrade_failure_returns_to_menu_without_losing_program(self):
        (self.f.assets/'shell-wol-linux-amd64').write_text('damaged download')
        output=self.terminal(answers='6\n1\ny\n\n0\n')
        self.assertIn('SHA256 校验失败',output)
        self.assertEqual(output.count('运行状态：'),2)
        self.assertIn('version=v1.2.3',(self.root/'.shell-wol-install').read_text())
    def test_uninstall_keeps_data_offline_and_exits_menu(self):
        self.f.tool('curl','#!/bin/sh\nexit 99\n')
        before=(self.root/'data/config.yaml').read_bytes()
        output=self.terminal(answers='7\n1\ny\n')
        self.assertEqual((self.root/'data/config.yaml').read_bytes(),before)
        for name in ('bin/shell-wol','menu.sh','uninstall.sh'):
            self.assertFalse((self.root/name).exists(),name)
        self.assertEqual(output.count('运行状态：'),1)
        self.assertNotIn('按回车返回',output)
        self.assertIn('status=uninstalled',(self.root/'.shell-wol-install').read_text())
    def test_purge_removes_managed_files_but_keeps_unknown_files(self):
        (self.root/'notes.txt').write_text('keep me')
        self.terminal('uninstall','2\ny\n')
        self.assertEqual((self.root/'notes.txt').read_text(),'keep me')
        for name in ('data/config.yaml','menu.sh','uninstall.sh','.shell-wol-install'):
            self.assertFalse((self.root/name).exists(),name)
    def test_uninstall_cancel_at_final_confirmation_keeps_files(self):
        self.terminal('uninstall','1\nn\n',code=1)
        self.assertTrue((self.root/'bin/shell-wol').exists())
        self.assertTrue((self.root/'uninstall.sh').exists())
    def test_uninstall_submenu_cancel_returns_to_main_menu(self):
        self.terminal(answers='7\n0\n0\n')
        self.assertTrue((self.root/'bin/shell-wol').exists())
    def test_maintenance_preserves_another_operation_lock(self):
        lock=self.root/'.install-lock';lock.mkdir()
        for action in ('upgrade','uninstall'):
            self.terminal(action,code=1)
            self.assertTrue(lock.is_dir())
            self.assertTrue((self.root/'bin/shell-wol').exists())
    def test_system_mode_requires_root_without_guessing_from_current_uid(self):
        state=self.root/'.shell-wol-install'
        state.write_text(state.read_text().replace('install_mode=user','install_mode=system'))
        self.f.tool('id','#!/bin/sh\necho 1000\n')
        self.assertIn('root',self.terminal('upgrade',code=1))
        self.f.tool('id','#!/bin/sh\necho 0\n')
        self.terminal('upgrade','1\ny\n')
        self.assertIn('install_mode=system',state.read_text())
    def test_legacy_unknown_mode_and_missing_offline_script_are_actionable(self):
        (self.root/'uninstall.sh').unlink()
        self.assertIn('本地卸载脚本缺失',self.terminal('uninstall',code=1))
        state=self.root/'.shell-wol-install'
        state.write_text(state.read_text().replace('install_mode=user\n',''))
        self.assertIn('旧安装记录',self.terminal('upgrade',code=1))
    def test_noninteractive_commands_do_not_modify_files(self):
        for action in ('upgrade','uninstall'):
            r=subprocess.run(['sh',str(self.root/'menu.sh'),action],env=self.env,capture_output=True,text=True)
            self.assertNotEqual(r.returncode,0)
            self.assertIn('交互终端',r.stderr)
        self.assertTrue((self.root/'bin/shell-wol').exists())

if __name__=='__main__':unittest.main()
