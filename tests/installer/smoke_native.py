"""Integration check with a real release-version Go binary, isolated download fixture.
Usage: python3 tests/installer/smoke_native.py /path/to/release-version-binary
"""
import json
import re
import secrets
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

from test_install import InstallerTests

binary = Path(sys.argv[1]).resolve()
version = subprocess.check_output([str(binary), 'version'], text=True).strip()
fixture = InstallerTests()
fixture.setUp()
try:
    fixture.make_release(version)
    shutil.copyfile(binary, fixture.assets / 'shell-wol-linux-amd64')
    fixture.manifest()
    fixture.run_install()
    installed = fixture.root / 'bin/shell-wol'
    data = fixture.root / 'data'
    with socket.socket() as free:
        free.bind(('127.0.0.1', 0))
        port = free.getsockname()[1]
    config = data / 'config.yaml'
    config.write_text(config.read_text().replace('127.0.0.1:9000', f'0.0.0.0:{port}'))
    log = fixture.base / 'server.log'
    with log.open('w') as stdout:
        process = subprocess.Popen([str(installed), 'serve', '--data-dir', str(data)],
                                   stdout=stdout, stderr=subprocess.DEVNULL)
        try:
            for _ in range(100):
                result = subprocess.run([str(installed), 'check', '--data-dir', str(data), '--running'],
                                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                if result.returncode == 0:
                    break
                time.sleep(.05)
            else:
                raise RuntimeError('uninitialized LAN service not ready')
            menu=fixture.root/'menu.sh'
            result=subprocess.run(['sh',str(menu),'status'],env=fixture.env,text=True,capture_output=True)
            assert result.returncode==0,result.stdout+result.stderr
            assert f'PID：{process.pid}' in result.stdout
            assert re.search(r'RSS.*[0-9.]+ MiB',result.stdout)
            assert 'Web：正常' in result.stdout
            # A process using this executable with another data dir must not match.
            result=subprocess.run(['sh','-c',
                '. "$1"; ROOT=$2;BIN=$ROOT/bin/shell-wol;DATA=$ROOT/other-data;find_pid',
                'test',str(menu),str(fixture.root)],env=dict(fixture.env,SHELL_WOL_MENU_LIBRARY='1'),
                text=True,capture_output=True)
            assert result.returncode==1,result.stdout+result.stderr
            token = re.search(r'^[0-9a-f]{64}$', log.read_text(), re.M).group()
            current=subprocess.check_output([str(installed),'token','--data-dir',str(data)],text=True).strip()
            assert current==token
            assert (data/'.setup-token').stat().st_mode & 0o777 == 0o600
            # SIGKILL must release the kernel lock even though files remain.
            process.kill();process.wait(timeout=10)
            assert (data/'.lock').is_file() and (data/'.setup-token').exists()
            stale=subprocess.run([str(installed),'token','--data-dir',str(data)],text=True,capture_output=True)
            assert stale.returncode!=0 and token not in stale.stdout+stale.stderr
            process=subprocess.Popen([str(installed),'serve','--data-dir',str(data)],stdout=stdout,stderr=subprocess.DEVNULL)
            for _ in range(100):
                result=subprocess.run([str(installed),'check','--data-dir',str(data),'--running'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                if result.returncode==0:break
                time.sleep(.05)
            else:raise RuntimeError('service did not recover after SIGKILL')
            fresh=subprocess.check_output([str(installed),'token','--data-dir',str(data)],text=True).strip()
            assert fresh!=token
            token=fresh
            menu_token=subprocess.run(['sh',str(menu),'token'],env=fixture.env,text=True,capture_output=True)
            assert menu_token.returncode==0 and menu_token.stdout.strip()==token
            duplicate=subprocess.run([str(installed),'init','--data-dir',str(data)],capture_output=True,text=True)
            assert duplicate.returncode!=0
            bind_data=fixture.base/'bind-failure'
            subprocess.run([str(installed),'init','--data-dir',str(bind_data)],check=True,stdout=subprocess.DEVNULL)
            bind_config=bind_data/'config.yaml'
            bind_config.write_text(bind_config.read_text().replace('127.0.0.1:9000',f'0.0.0.0:{port}'))
            failed=subprocess.run([str(installed),'serve','--data-dir',str(bind_data)],text=True,capture_output=True)
            assert failed.returncode!=0 and not (bind_data/'.setup-token').exists()
            subprocess.run([str(installed),'lock','--data-dir',str(bind_data)],check=True,stdout=subprocess.DEVNULL)

            base = f'http://127.0.0.1:{port}'
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(base + '/api/session') as response:
                state = json.load(response)
                assert state['setup_required'] and not state['authenticated']
                assert token not in json.dumps(state)
            request = urllib.request.Request(base + '/api/setup', method='POST',
                data=json.dumps({'token': token, 'password': secrets.token_urlsafe(20)}).encode(),
                headers={'Content-Type': 'application/json', 'Origin': base})
            with opener.open(request) as response:
                assert response.status == 200
            subprocess.run([str(installed), 'check', '--data-dir', str(data), '--require-password', '--running'],
                           check=True, stdout=subprocess.DEVNULL)
            assert not (data/'.setup-token').exists()
            assert '管理员已初始化' in subprocess.check_output([str(installed),'token','--data-dir',str(data)],text=True)
            fixture.run_install(success=False)  # Manual process owns the data lock.
            blocked = subprocess.run(['sh', str(Path(__file__).resolve().parents[2] / 'scripts/uninstall.sh'),
                                      '--user', '--install-dir', str(fixture.root), '--yes', '--purge'],
                                     text=True, capture_output=True)
            assert blocked.returncode != 0 and installed.exists()
            assert (data / '.lock').exists()
        finally:
            process.terminate()
            process.wait(timeout=10)
    assert subprocess.run(['sh',str(menu),'status'],env=fixture.env,
                          stdout=subprocess.DEVNULL).returncode==1
    # Real menu + real Go serve; only the system service commands are private.
    unit=fixture.service/'shell-wol.service'
    unit.write_text(f'ExecStart="{installed}" serve --data-dir "{data}"\n')
    fixture.tool('id','#!/bin/sh\necho 0\n')
    fixture.tool('sleep','#!/bin/sh\nexec /bin/sleep "$@"\n')
    fixture.tool('systemctl',r'''#!/bin/sh
root="$FIXTURE_ROOT/Install With Spaces"
case "$1" in
start)
 "$root/bin/shell-wol" serve --data-dir "$root/data" >"$FIXTURE_SERVICE/core.log" 2>&1 </dev/null &
 echo "$!" >"$FIXTURE_SERVICE/core.pid";;
stop)
 if [ -f "$FIXTURE_SERVICE/core.pid" ]; then
  kill "$(cat "$FIXTURE_SERVICE/core.pid")" || exit 1
  rm -f "$FIXTURE_SERVICE/core.pid"
 fi;;
*) exit 2;;
esac
''')
    def control(action):
        command=r'''. "$1"
setup() {
 ROOT=$TEST_INSTALL;BIN=$ROOT/bin/shell-wol;DATA=$ROOT/data
 SERVICE=systemd;UNIT=$FIXTURE_SERVICE/shell-wol.service
}
main "$3"
'''
        return subprocess.run(['sh','-c',command,'test',str(menu),str(fixture.root),action],
            env=dict(fixture.env,SHELL_WOL_MENU_LIBRARY='1',TEST_INSTALL=str(fixture.root)),
            text=True,capture_output=True,timeout=30)
    try:
        for action in ('start','restart','stop'):
            result=control(action)
            assert result.returncode==0,(action,result.stdout,result.stderr)
    finally:
        pid=fixture.service/'core.pid'
        if pid.exists():
            result=control('stop')
            assert result.returncode==0,result.stdout+result.stderr
    before = config.read_bytes()
    fixture.run_install()
    assert config.read_bytes() == before
    assert (fixture.root / 'backup/shell-wol.previous').exists()
    assert not (fixture.root / '.install-lock').exists()
    assert (data / '.lock').is_file()
    subprocess.run([str(installed),'lock','--data-dir',str(data)],check=True,stdout=subprocess.DEVNULL)
    uninstall = ['sh', str(Path(__file__).resolve().parents[2] / 'scripts/uninstall.sh'),
                 '--user', '--install-dir', str(fixture.root), '--yes']
    subprocess.run(uninstall, check=True, stdout=subprocess.DEVNULL)
    assert not installed.exists() and config.read_bytes() == before
    fixture.run_install()
    assert installed.exists() and config.read_bytes() == before
    subprocess.run(uninstall + ['--purge'], check=True, stdout=subprocess.DEVNULL)
    assert not fixture.root.exists()
    # A failed upgrade from the legacy directory-lock binary must restore a
    # layout that the old binary can still open.
    fixture.make_release('v0.0.1-test')
    fixture.run_install()
    before={n:(fixture.root/n).read_bytes() for n in ('bin/shell-wol','.shell-wol-install','data/config.yaml')}
    fixture.make_release(version)
    shutil.copyfile(binary,fixture.assets/'shell-wol-linux-amd64');fixture.manifest()
    fixture.tool('mv',r'''#!/bin/sh
for arg in "$@"; do
 case "$arg" in */.shell-wol-install)
  if [ ! -e "$FIXTURE_ROOT/state-failed" ]; then touch "$FIXTURE_ROOT/state-failed";exit 1;fi;;
 esac
done
exec /bin/mv "$@"
''')
    fixture.run_install(success=False)
    for n,content in before.items():assert (fixture.root/n).read_bytes()==content,n
    assert not (fixture.root/'data/.lock').exists()
    subprocess.run(uninstall+['--purge'],env=fixture.env,check=True,stdout=subprocess.DEVNULL)
    print('PASS: kernel lock/SIGKILL recovery, private token lifecycle, menu, bind failure, upgrade rollback and uninstall/reinstall/purge')
finally:
    fixture.tearDown()
