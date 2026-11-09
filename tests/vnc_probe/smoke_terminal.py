"""POSIX PTY smoke check for a built mac-vnc-probe; no real Mac or credentials.

Usage: python3 tests/vnc_probe/smoke_terminal.py /path/to/mac-vnc-probe
"""
import os
from pathlib import Path
import pty
import select
import signal
import socket
import subprocess
import sys
import termios
import threading
import time


binary = Path(sys.argv[1]).resolve()


def check_input(action, port=1):
    master, slave = pty.openpty()
    original = termios.tcgetattr(slave)
    output = bytearray()
    process = subprocess.Popen(
        [str(binary), '--host', '127.0.0.1', '--port', str(port),
         '--user', 'fixture-user', '--stage', 'auth', '--timeout', '2s'],
        stdin=slave, stdout=slave, stderr=slave, start_new_session=True)

    def drain_until(predicate, seconds=5):
        deadline = time.monotonic() + seconds
        while not predicate():
            if time.monotonic() >= deadline:
                raise AssertionError('terminal probe did not finish expected step')
            ready, _, _ = select.select([master], [], [], .05)
            if ready:
                output.extend(os.read(master, 65536))

    try:
        drain_until(lambda: 'Mac 密码'.encode() in output)
        assert not termios.tcgetattr(slave)[3] & termios.ECHO, 'password echo is enabled'
        if action == 'password':
            os.write(master, b'fixture-password\r')
            expected = 1  # Fake server deliberately does not offer DH(30).
        elif action == 'ctrl-c':
            os.write(master, b'\x03')
            expected = 130
        else:
            process.send_signal(signal.SIGINT)
            expected = 130
        drain_until(lambda: process.poll() is not None)
        while select.select([master], [], [], .05)[0]:
            output.extend(os.read(master, 65536))
        assert process.returncode == expected, (process.returncode, output.decode(errors='replace'))
        assert b'fixture-password' not in output, 'password was echoed or logged'
        assert termios.tcgetattr(slave) == original, 'terminal mode was not restored'
        if action == 'password':
            assert '未提供 DH(30)'.encode() in output
    finally:
        if process.poll() is None:
            process.terminate()
        process.wait(timeout=5)
        os.close(master)
        os.close(slave)


with socket.socket() as listener:
    listener.bind(('127.0.0.1', 0))
    listener.listen(1)
    listener.settimeout(5)
    errors = []

    def fake_server():
        try:
            with listener.accept()[0] as conn:
                conn.settimeout(5)
                conn.sendall(b'RFB 003.008\n')
                reply = bytearray()
                while len(reply) < 12:
                    chunk = conn.recv(12-len(reply))
                    assert chunk
                    reply.extend(chunk)
                assert reply == b'RFB 003.008\n'
                conn.sendall(b'\x01\x01')
        except Exception as error:
            errors.append(error)

    worker = threading.Thread(target=fake_server, daemon=True)
    worker.start()
    check_input('password', listener.getsockname()[1])
    worker.join(timeout=5)
    assert not worker.is_alive() and not errors, errors

check_input('ctrl-c')
check_input('signal')
print('PASS: hidden password input, cancellation, no secret logging, terminal restoration')
