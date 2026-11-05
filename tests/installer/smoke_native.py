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
    print('PASS: native install, LAN setup, readiness, runtime lock, upgrade, data-preserving uninstall/reinstall, purge')
finally:
    fixture.tearDown()
