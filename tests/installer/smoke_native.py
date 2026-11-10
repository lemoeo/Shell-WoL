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
    config.write_text(config.read_text().replace('127.0.0.1:8080', f'0.0.0.0:{port}'))
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
    assert not (data / '.lock').exists()
    uninstall = ['sh', str(Path(__file__).resolve().parents[2] / 'scripts/uninstall.sh'),
                 '--user', '--install-dir', str(fixture.root), '--yes']
    subprocess.run(uninstall, check=True, stdout=subprocess.DEVNULL)
    assert not installed.exists() and config.read_bytes() == before
    fixture.run_install()
    assert installed.exists() and config.read_bytes() == before
    subprocess.run(uninstall + ['--purge'], check=True, stdout=subprocess.DEVNULL)
    assert not fixture.root.exists()
    print('PASS: native install, LAN setup, PID/RSS, menu service lifecycle, runtime lock, upgrade, uninstall/reinstall, purge')
finally:
    fixture.tearDown()
