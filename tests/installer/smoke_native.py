"""Integration check with a real release-version Go binary, isolated download fixture.
Usage: python3 tests/installer/smoke_native.py /path/to/release-version-binary
"""
import secrets
import shutil
import socket
import subprocess
import sys
import time
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
    subprocess.run([str(installed), 'init', '--data-dir', str(data), '--password-stdin'],
                   input=secrets.token_urlsafe(20).encode(), check=True, stdout=subprocess.DEVNULL)
    with socket.socket() as free:
        free.bind(('127.0.0.1', 0))
        port = free.getsockname()[1]
    config = data / 'config.yaml'
    config.write_text(config.read_text().replace('127.0.0.1:8080', f'127.0.0.1:{port}'))
    before = config.read_bytes()
    process = subprocess.Popen([str(installed), 'serve', '--data-dir', str(data)],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(100):
            result = subprocess.run([str(installed), 'check', '--data-dir', str(data),
                                     '--require-password', '--running'],
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if result.returncode == 0:
                break
            time.sleep(.05)
        else:
            raise RuntimeError('service not ready')
        fixture.run_install(success=False)  # Manual process owns the data lock.
    finally:
        process.terminate()
        process.wait(timeout=10)
    fixture.run_install()
    assert config.read_bytes() == before
    assert (fixture.root / 'backup/shell-wol.previous').exists()
    assert not (fixture.root / '.install-lock').exists()
    assert not (data / '.lock').exists()
    print('PASS: native install, password setup, HTTP readiness, active-process lock, repeat upgrade')
finally:
    fixture.tearDown()
