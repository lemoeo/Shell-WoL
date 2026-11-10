"""Persistent router boot lifecycle; all UCI, ubus and init files are isolated."""
import hashlib
import json
import os
import subprocess
import time
import unittest

import test_install as fixture


UCI = r'''#!/usr/bin/env python3
import json, os, pathlib, shlex, sys
root=pathlib.Path(os.environ['FIXTURE_ROOT'])
db=root/'uci.json'; state=json.loads(db.read_text())
saved=state['saved']; staged=state['staged']; current=dict(saved)
for k,v in staged.items():
 if v is None:current.pop(k,None)
 else:current[k]=v
args=[a for a in sys.argv[1:] if a not in ('-q','-m')]; command=args[0]; key=args[1]
def match(k):return k==key or k.startswith(key+'.')
if command=='get':
 if key not in current:sys.exit(1)
 print(current[key]);sys.exit(0)
if command=='show':
 for k,v in sorted(current.items()):
  if match(k):print(k+'='+repr(v))
 sys.exit(0)
if command=='export':
 print('package firewall\n')
 for k,v in sorted(current.items()):
  if k.count('.')!=1:continue
  print('config '+v+' '+repr(k.split('.')[1]))
  # Real UCI always quotes config names, including simple identifiers.
  for option,value in sorted(current.items()):
   if option.startswith(k+'.'):print('\toption '+option[len(k)+1:]+' '+shlex.quote(value))
  print()
 sys.exit(0)
if command=='changes':
 for k in sorted(set(saved)|set(current)):
  if match(k) and saved.get(k)!=current.get(k):
   print(('-'+k) if k not in current else k+'='+repr(current[k]))
 sys.exit(0)
if command=='set':
 k,v=key.split('=',1);staged[k]=v
elif command=='delete':
 keys=[k for k in current if match(k)]
 if not keys:sys.exit(1)
 for k in keys:staged[k]=None
elif command=='revert':
 for k in list(staged):
  if match(k):del staged[k]
elif command=='import':
 section=None
 for line in sys.stdin:
  words=shlex.split(line)
  if not words or words[0]=='package':continue
  if words[0]=='config':section='firewall.'+words[2];staged[section]=words[1]
  elif words[0]=='option':staged[section+'.'+words[1]]=words[2]
  else:sys.exit(2)
elif command=='commit':
 fail=root/'commit-failed'
 if os.environ.get('UCI_FAIL_COMMIT_ONCE')=='1' and not fail.exists():
  fail.touch();sys.exit(1)
 state={'saved':current,'staged':{}}
 with (root/'service/events').open('a') as f:f.write('uci commit\n')
else:sys.exit(2)
db.write_text(json.dumps(state))
'''

PROCD = r'''#!/bin/sh
INSTALL_ROOT='@ROOT@'
folder=$FIXTURE_SERVICE
printf '%s\n' "$1" >> "$folder/calls"
case "$1" in
running) [ "${STATUS_ERROR:-0}" != 1 ] || exit 2; [ -f "$folder/active" ];;
start)
 [ "${START_FAIL:-0}" != 1 ] || exit 1
 if [ "${SLOW_START:-0}" = 1 ]; then /bin/sleep 0.3; fi
 printf 'start\n' >> "$folder/events"
 touch "$folder/active";;
stop)
 printf 'stop\n' >> "$folder/events"
 [ "${STOP_FAIL:-0}" != 1 ] || exit 1
 rm -f "$folder/active";;
enable) touch "$folder/enabled";;
disable) rm -f "$folder/enabled";;
enabled) [ -f "$folder/enabled" ];;
*) exit 2;;
esac
'''


class BootTests(unittest.TestCase):
    def setUp(self):
        self.f = fixture.InstallerTests(); self.f.setUp()
        self.root = self.f.base/'router-install'; self.service = self.f.service
        self.db = self.f.base/'uci.json'
        self.original = {
            'firewall.defaults': 'defaults',
            'firewall.ShellCrash': 'include',
            'firewall.ShellCrash.path': '/data/shellcrash_init.sh',
            'firewall.lan.input': 'ACCEPT',
        }
        self.db.write_text(json.dumps({'saved': self.original, 'staged': {}}))
        self.f.tool('uci', UCI)
        self.f.tool('ubus', '#!/bin/sh\n[ "${UBUS_FAIL:-0}" != 1 ]\n')
        self.f.tool('id', '#!/bin/sh\n[ "$1" != -u ] || { echo 0; exit 0; }\nexec /usr/bin/id "$@"\n')
        self.f.tool('logger', '#!/bin/sh\nprintf "%s\\n" "$*" >> "$FIXTURE_SERVICE/log"\n')
        self.unit = self.service/'shell-wol'
        self.env = self.f.env.copy()
        self.env['SHELL_WOL_UNINSTALL_LIBRARY'] = '1'
        self.make_release('v1.2.3')

    def tearDown(self): self.f.tearDown()

    def make_release(self, version):
        self.f.make_release(version)
        (self.f.template_dir/'shell-wol.openwrt').write_text(PROCD)
        boot=self.f.template_dir.parent/'scripts/boot.sh'
        boot.write_text(boot.read_text().replace('SERVICE_FILE=/etc/init.d/shell-wol',
            'SERVICE_FILE="$FIXTURE_SERVICE/shell-wol"').replace(
            'BOOT_LOCK=/tmp/shell-wol-boot.lock','BOOT_LOCK="$FIXTURE_SERVICE/boot.lock"'))
        self.f.template_manifest()

    def install(self, *args, mode='firewall', success=True):
        prefix = '. "$1"; UNINSTALL_SOURCE=$1; shift; BOOT_LOCK="$FIXTURE_SERVICE/boot.lock"; '
        prefix += 'menu_profile_path() { echo "$FIXTURE_SERVICE/profile"; }; '
        prefix += 'detect_firmware() { echo openwrt; }; '
        prefix += 'choose_service() { SERVICE=procd; SERVICE_FILE="$FIXTURE_SERVICE/shell-wol"; SERVICE_USER=; SERVICE_GROUP=; LAUNCH_DOMAIN=; }; '
        return self.invoke(prefix+'main "$@"', fixture.INSTALLER,
                           '--arch', 'amd64', '--install-dir', str(self.root),
                           '--service', '--boot-mode', mode, '--yes', *args, success=success)

    def uninstall(self, *args, success=True):
        prefix = '. "$1"; UNINSTALL_SOURCE=$1; shift; BOOT_LOCK="$FIXTURE_SERVICE/boot.lock"; '
        prefix += 'resolve_service() { SERVICE_FILE="$FIXTURE_SERVICE/shell-wol"; LAUNCH_DOMAIN=; }; '
        return self.invoke(prefix+'main "$@"', fixture.REPO/'scripts/uninstall.sh',
                           '--install-dir', str(self.root), '--yes', *args, success=success)

    def invoke(self, command, script, *args, success=True):
        r = subprocess.run(['sh', '-c', command, 'test', str(script), *args],
                           env=self.env, text=True, capture_output=True, timeout=20)
        self.assertEqual(r.returncode == 0, success, r.stdout+r.stderr)
        return r

    def boot(self, command='run', success=True):
        r = subprocess.run(['sh', str(self.root/'boot.sh'), command], env=self.env,
                           text=True, capture_output=True, timeout=10)
        self.assertEqual(r.returncode == 0, success, r.stdout+r.stderr)
        return r

    def saved(self): return json.loads(self.db.read_text())['saved']

    def starts(self):
        p = self.service/'events'
        return p.read_text().splitlines().count('start') if p.exists() else 0

    def reset_etc(self):
        self.unit.unlink()
        (self.service/'active').unlink(missing_ok=True)
        (self.service/'enabled').unlink(missing_ok=True)

    def test_boot_download_uses_main_and_is_verified_before_stop(self):
        self.install()
        before={n:(self.root/n).read_bytes() for n in ('boot.sh','.shell-wol-install','bin/shell-wol')}
        starts=self.starts()
        self.make_release('v1.2.4')
        self.env['CORRUPT_CDN_SUFFIX']='/scripts/boot.sh'
        self.install('--source','jsdelivr',success=False)
        for n,data in before.items():self.assertEqual((self.root/n).read_bytes(),data)
        self.assertTrue((self.service/'active').exists());self.assertEqual(self.starts(),starts)
        requests=(self.f.base/'downloads').read_text().splitlines()
        self.assertIn('https://cdn.jsdelivr.net/gh/lemoeo/Shell-WoL@main/scripts/boot.sh',requests)
        del self.env['CORRUPT_CDN_SUFFIX']
        self.install('--source','jsdelivr')
        self.assertNotIn('@ROOT@',(self.root/'boot.sh').read_text())

    def test_missing_main_boot_fails_before_modifying_installation(self):
        self.install()
        before=(self.root/'.shell-wol-install').read_bytes()
        self.make_release('v1.2.4')
        (self.f.template_dir.parent/'scripts/boot.sh').unlink()
        result=self.install(success=False)
        self.assertIn('无法下载 main 的 boot.sh',result.stderr)
        self.assertEqual((self.root/'.shell-wol-install').read_bytes(),before)
        self.assertTrue((self.service/'active').exists())

    def test_native_procd_upgrade_preserves_all_service_states(self):
        self.install(mode='native')
        for running,enabled in ((False,False),(False,True),(True,False),(True,True)):
            with self.subTest(running=running,enabled=enabled):
                for name,present in (('active',running),('enabled',enabled)):
                    p=self.service/name
                    if present:p.touch()
                    else:p.unlink(missing_ok=True)
                self.install(mode='native')
                self.assertEqual((self.service/'active').exists(),running)
                self.assertEqual((self.service/'enabled').exists(),enabled)

    def test_registration_and_reset_recovery(self):
        self.install()
        self.assertTrue((self.service/'active').exists())
        self.assertFalse((self.service/'enabled').exists())
        self.assertIn('schema=2', (self.root/'.shell-wol-install').read_text())
        self.assertEqual(self.saved()['firewall.ShellWoL.path'], str(self.root/'boot.sh'))
        self.assertEqual(self.saved()['firewall.ShellWoL.enabled'], '1')
        for k, v in self.original.items(): self.assertEqual(self.saved()[k], v)
        self.reset_etc(); self.boot()
        self.assertTrue(self.unit.exists()); self.assertTrue((self.service/'active').exists())
        count = self.starts(); self.boot(); self.assertEqual(self.starts(), count)
        self.assertFalse((self.service/'boot.lock').exists())

    def test_fallback_checksums_cover_install_boot_recovery_and_uninstall(self):
        from test_checksums import configure_backends
        for chosen in ('busybox', 'shasum', 'openssl'):
            with self.subTest(chosen=chosen):
                configure_backends(self.f, self.env, {chosen: 'ok'})
                self.install()
                self.reset_etc(); self.boot()
                self.assertTrue((self.service/'active').exists())
                self.uninstall('--purge')
                self.assertFalse(self.root.exists())

    def test_missing_checksum_backend_prevents_boot_restoration(self):
        from test_checksums import configure_backends
        self.install(); self.reset_etc()
        configure_backends(self.f, self.env, {})
        result = self.boot(success=False)
        self.assertIn('SHA256', result.stderr)
        self.assertFalse(self.unit.exists())
        self.assertFalse((self.service/'active').exists())
        self.assertFalse((self.service/'boot.lock').exists())

    def test_testingcf_install_with_busybox_recovers_and_uninstalls(self):
        from test_checksums import configure_backends
        configure_backends(self.f, self.env, {'busybox': 'ok'})
        self.env['FIXTURE_CDN_BASE'] = 'https://testingcf.jsdelivr.net'
        self.install('--source', 'jsdelivr', '--cdn-base', 'https://testingcf.jsdelivr.net')
        requests = (self.f.base/'downloads').read_text().splitlines()
        self.assertTrue(all(u.startswith('https://testingcf.jsdelivr.net/gh/') for u in requests))
        self.reset_etc(); self.boot()
        self.assertTrue((self.service/'active').exists())
        self.uninstall('--purge'); self.assertFalse(self.root.exists())

    def test_concurrent_firewall_triggers_start_once(self):
        self.install(); self.reset_etc(); self.env['SLOW_START'] = '1'
        count = self.starts()
        self.boot('start'); self.boot('start')
        deadline = time.monotonic()+5
        while time.monotonic() < deadline:
            if (self.service/'active').exists() and not (self.service/'boot.lock').exists(): break
            time.sleep(0.02)
        self.assertTrue((self.service/'active').exists())
        self.assertEqual(self.starts(), count+1)

    def test_fw3_sourced_include_returns_quickly_and_recovers(self):
        self.install(); self.reset_etc(); self.env['SLOW_START'] = '1'
        # fw3 uses system("config() { ...; }; . <unquoted path>"). Some vendor
        # shells additionally pass the firewall action into the sourced script.
        include = 'ROOT=parent; config() { exit 1; }; . '+str(self.root/'boot.sh')
        include += '; test "$ROOT" = parent && : "$unset_in_parent"'
        r = subprocess.run(['sh', '-c', include, 'firewall', 'boot'], env=self.env,
                           capture_output=True, text=True, timeout=2)
        self.assertEqual(r.returncode, 0, r.stdout+r.stderr)
        deadline = time.monotonic()+5
        while time.monotonic() < deadline:
            if (self.service/'active').exists() and not (self.service/'boot.lock').exists(): break
            time.sleep(0.02)
        self.assertTrue((self.service/'active').exists())

    def test_firewall_path_with_spaces_is_rejected_before_changes(self):
        self.root = self.f.root
        result = self.install(success=False)
        self.assertIn('不能包含空格', result.stderr)
        self.assertEqual(self.saved(), self.original)
        self.assertFalse(self.root.exists())

    def test_disable_survives_reset_and_upgrade(self):
        self.install(); self.boot('disable'); self.reset_etc()
        self.boot(); self.assertFalse(self.unit.exists())
        self.make_release('v1.2.4'); self.install(mode='auto')
        self.assertEqual(self.saved()['firewall.ShellWoL.enabled'], '0')
        self.assertFalse((self.service/'active').exists())
        self.assertTrue((self.root/'.autostart-disabled').exists())
        self.boot('status', success=False)
        self.boot('enable'); self.boot()
        self.assertTrue((self.service/'active').exists())

    def test_old_native_install_can_migrate_and_return_to_native(self):
        self.install(mode='native')
        self.assertNotIn('firewall.ShellWoL', self.saved())
        self.assertTrue((self.service/'enabled').exists())
        self.install()
        self.assertFalse((self.service/'enabled').exists())
        self.assertIn('firewall.ShellWoL', self.saved())
        self.install(mode='native')
        self.assertNotIn('firewall.ShellWoL', self.saved())
        self.assertFalse((self.root/'boot.sh').exists())
        self.assertTrue((self.service/'enabled').exists())

    def test_auto_selection_uses_xiaomi_marker_and_preserves_explicit_choice(self):
        command = '. "$1"; ROOT=/data/shell-wol; SERVICE=procd; FIRMWARE=xiaomi; choose_boot_mode; echo "$BOOT_MODE"'
        r = self.invoke(command, fixture.INSTALLER)
        self.assertEqual(r.stdout.strip(), 'firewall')
        self.install(mode='native'); self.install(mode='auto')
        self.assertFalse((self.root/'boot.sh').exists())

    def test_fw4_include_is_marked_compatible(self):
        self.f.tool('fw4', '#!/bin/sh\nexit 0\n')
        self.install(); self.assertEqual(self.saved()['firewall.ShellWoL.fw4_compatible'], '1')

    def test_foreign_include_and_pending_changes_are_not_overwritten(self):
        state = {'saved': dict(self.original, **{'firewall.ShellWoL': 'include',
                 'firewall.ShellWoL.path': '/data/other/boot.sh'}), 'staged': {}}
        self.db.write_text(json.dumps(state)); self.install(success=False)
        self.assertEqual(json.loads(self.db.read_text()), state)
        state = {'saved': self.original, 'staged': {'firewall.lan.input': 'DROP'}}
        self.db.write_text(json.dumps(state)); self.install(success=False)
        self.assertEqual(json.loads(self.db.read_text()), state)
        self.assertFalse(self.root.exists())

    def test_failed_start_restores_binary_hook_service_and_include(self):
        self.install()
        before = {n: (self.root/n).read_bytes() for n in
                  ('boot.sh', 'service/shell-wol.procd', '.shell-wol-install', 'bin/shell-wol', 'data/config.yaml')}
        old_uci = self.saved()
        self.make_release('v1.2.4'); self.env['FAIL_RUNNING_VERSION'] = 'v1.2.4'
        self.install(success=False)
        for n, content in before.items(): self.assertEqual((self.root/n).read_bytes(), content, n)
        self.assertEqual(self.saved(), old_uci)
        self.assertTrue((self.service/'active').exists())
        self.assertFalse((self.root/'.autostart-disabled').exists())

    def test_failed_native_migration_restores_complete_owned_section(self):
        self.install()
        state = json.loads(self.db.read_text())
        state['saved']['firewall.ShellWoL.custom'] = 'keep this option'
        self.db.write_text(json.dumps(state))
        self.make_release('v1.2.4'); self.env['FAIL_RUNNING_VERSION'] = 'v1.2.4'
        self.install(mode='native', success=False)
        self.assertEqual(self.saved(), state['saved'])
        self.boot('status')

    def test_failed_fresh_commit_cleans_hook_and_can_retry(self):
        self.env['UCI_FAIL_COMMIT_ONCE'] = '1'
        self.install(success=False)
        self.assertEqual(self.saved(), self.original)
        self.assertFalse((self.root/'boot.sh').exists())
        self.assertFalse(self.unit.exists())
        self.install(); self.assertTrue((self.service/'active').exists())

    def test_boot_conflict_runtime_lock_and_install_lock_do_not_start(self):
        self.install(); self.reset_etc()
        self.unit.write_text('#!/bin/sh\nINSTALL_ROOT=other\n'); self.unit.chmod(0o755)
        self.boot(success=False); self.assertIn('other', self.unit.read_text())
        self.unit.unlink(); (self.root/'data/.lock').mkdir()
        self.boot(success=False); self.assertTrue((self.root/'data/.lock').exists())
        (self.root/'data/.lock').rmdir(); (self.root/'.install-lock').mkdir()
        self.boot(); self.assertFalse((self.service/'active').exists())

    def test_readiness_wait_is_bounded(self):
        self.install(); self.reset_etc(); self.env['UBUS_FAIL'] = '1'
        self.f.tool('date', '''#!/usr/bin/env python3
import os,pathlib
p=pathlib.Path(os.environ['FIXTURE_ROOT'])/'clock'
n=int(p.read_text())+20 if p.exists() else 0
p.write_text(str(n));print(n)
''')
        self.boot(success=False)
        self.assertFalse(self.unit.exists())
        self.assertFalse((self.service/'boot.lock').exists())

    def test_vendor_running_success_does_not_skip_real_start(self):
        # Old vendor rc.common may report running even without an instance.
        self.f.env['REQUIRE_ACTIVE']='1';self.env['REQUIRE_ACTIVE']='1'
        p=self.f.template_dir/'shell-wol.openwrt'
        p.write_text(p.read_text().replace('running) [ "${STATUS_ERROR:-0}" != 1 ] || exit 2; [ -f "$folder/active" ];;',
                                          'running) exit 0;;'))
        self.f.template_manifest()
        self.install();self.reset_etc()
        count=self.starts()
        self.boot()
        self.assertTrue((self.service/'active').exists())
        self.assertEqual(self.starts(),count+1)

    def test_uninstall_after_reset_detaches_before_stop_and_keeps_data(self):
        self.install(); self.reset_etc(); (self.service/'active').touch()
        before = (self.root/'data/config.yaml').read_bytes()
        (self.service/'events').write_text('')
        self.uninstall()
        self.assertEqual((self.service/'events').read_text().splitlines()[:2], ['uci commit', 'stop'])
        self.assertEqual(self.saved(), self.original)
        self.assertFalse(self.unit.exists()); self.assertFalse((self.root/'boot.sh').exists())
        self.assertEqual((self.root/'data/config.yaml').read_bytes(), before)
        self.uninstall(); self.install(); self.assertTrue((self.service/'active').exists())

    def test_uninstall_dry_run_with_missing_unit_does_not_restore_it(self):
        self.install(); self.reset_etc()
        before = self.db.read_bytes()
        files = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        result = self.uninstall('--dry-run')
        self.assertIn('firewall.ShellWoL', result.stdout)
        self.assertFalse(self.unit.exists()); self.assertEqual(self.db.read_bytes(), before)
        self.assertEqual({str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, files)

    def test_uninstall_conflicting_include_and_stop_failure_preserve_program(self):
        self.install()
        state = json.loads(self.db.read_text()); state['saved']['firewall.ShellWoL.path'] = '/data/foreign/boot.sh'
        self.db.write_text(json.dumps(state)); self.uninstall(success=False)
        self.assertEqual(json.loads(self.db.read_text()), state)
        state['saved']['firewall.ShellWoL.path'] = str(self.root/'boot.sh')
        self.db.write_text(json.dumps(state)); self.env['STOP_FAIL'] = '1'
        self.uninstall(success=False)
        self.assertNotIn('firewall.ShellWoL', self.saved())
        self.assertTrue((self.root/'bin/shell-wol').exists())
        self.assertTrue((self.root/'.autostart-disabled').exists())
        del self.env['STOP_FAIL']; self.uninstall('--purge')
        self.assertFalse(self.root.exists())


if __name__ == '__main__': unittest.main(verbosity=2)
