#!/usr/bin/python3
"""Fail closed before the Mini runner or a job writes build data."""
import fcntl
import os
import pathlib
import plistlib
import shutil
import subprocess
import sys

HDD = pathlib.Path('/Volumes/HDD-2TB')
HDD_UUID = 'FC7C9C7F-4C4E-33E0-A051-14384E7BBFE7'
ROOT = HDD / 'partyline-ci'
MARKER = pathlib.Path('/Users/darcy/partyline-ci-control/failure-notified')
GIB = 1024 ** 3


class StorageError(Exception):
    pass


def plist(command):
    result = subprocess.run(command, capture_output=True, timeout=20)
    if result.returncode:
        raise StorageError('cannot verify storage identity')
    return plistlib.loads(result.stdout)


def check(info=plist, usage=shutil.disk_usage, reserves=True):
    drive = info(['/usr/sbin/diskutil', 'info', '-plist', str(HDD)])
    if drive.get('VolumeUUID') != HDD_UUID or drive.get('MountPoint') != str(HDD):
        raise StorageError('expected external HDD is not mounted')
    if ROOT.resolve() != ROOT:
        raise StorageError('CI storage path contains a symbolic link')
    for child in ('work', 'cache', 'tmp', 'logs'):
        path = ROOT / child
        if path.resolve() != path:
            raise StorageError('CI data path contains a symbolic link')
    if not reserves:
        return
    if usage('/').free < 25 * GIB:
        raise StorageError('OS disk has less than 25 GiB free')
    if usage(str(HDD)).free < 100 * GIB:
        raise StorageError('external HDD has less than 100 GiB free')


def notify_once(message):
    try:
        fd = os.open(str(MARKER), os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except FileExistsError:
        return
    with os.fdopen(fd, 'w') as marker:
        marker.write(message + '\n')
    try:
        subprocess.run([
            '/usr/bin/ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5',
            '-o', 'UserKnownHostsFile=/Users/darcy/partyline-ci-control/known_hosts',
            '-o', 'UserKnownHostsFile=/Users/darcy/partyline-ci-control/known_hosts',
            'darcy@192.168.1.170', '/usr/local/bin/ptln', 'chat', 'send',
            "'" + message + "'",
        ], capture_output=True, timeout=10)
    except (OSError, subprocess.SubprocessError):
        pass


def main():
    try:
        check()
        with (ROOT / '.cleanup-lock').open('a') as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
            except BlockingIOError:
                raise StorageError('weekly cache cleanup is active')
    except (StorageError, OSError, ValueError, subprocess.SubprocessError) as error:
        # Only fixed messages: never dump command arguments, environment or credentials.
        reason = str(error) if isinstance(error, StorageError) else (
            'storage verification failed (' + type(error).__name__ +
            ', errno ' + str(getattr(error, 'errno', None)) + ')')
        message = 'Mini CI stopped: ' + reason + '. No OS-disk fallback.'
        print('::error::' + message, flush=True)
        notify_once(message)
        return 1
    MARKER.unlink(missing_ok=True)
    print('Mini CI external storage and free-space reserves verified.')
    if sys.argv[1:] == ['--launch']:
        # Runner diagnostics already rotate in external _diag; do not duplicate
        # the same unbounded service output in a second log.
        fd = os.open(os.devnull, os.O_WRONLY)
        os.dup2(fd, 1)
        os.dup2(fd, 2)
        os.close(fd)
        os.chdir('/Users/darcy/actions-runner')
        os.execv('./runsvc.sh', ['./runsvc.sh'])
    return 0


if __name__ == '__main__':
    sys.exit(main())
