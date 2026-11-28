#!/usr/bin/python3
"""Bound Netdata logs in place, preserving collector file descriptors."""
import gzip
import json
import os
import pathlib
import stat
import sys
import time

import storage_guard

CONTROL = pathlib.Path('/Users/darcy/partyline-ci-control')
LOGS = pathlib.Path('/opt/homebrew/var/log/netdata')
LIMIT = 10 * 1024 * 1024


def trim(log, limit=LIMIT):
    if not log.exists():
        return False
    fd = os.open(str(log), os.O_RDWR | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            raise RuntimeError('unexpected log type')
        if info.st_size <= limit:
            return False
        os.lseek(fd, -limit, os.SEEK_END)
        tail = os.read(fd, limit)
        # Only our three bounded samples are rotated; the live file never moves.
        oldest = log.with_name(log.name + '.partyline-3.gz')
        oldest.unlink(missing_ok=True)
        for number in (2, 1):
            previous = log.with_name(log.name + '.partyline-%d.gz' % number)
            following = log.with_name(log.name + '.partyline-%d.gz' % (number + 1))
            if previous.is_symlink() or following.is_symlink():
                raise RuntimeError('unexpected sample symlink')
            if previous.exists():
                previous.replace(following)
        sample = log.with_name(log.name + '.partyline-1.gz')
        sample_fd = os.open(str(sample), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(sample_fd, 'wb') as output:
            output.write(gzip.compress(tail))
        os.ftruncate(fd, 0)
        os.fsync(fd)
        return True
    finally:
        os.close(fd)


def main():
    try:
        if LOGS.resolve() != LOGS:
            raise RuntimeError('unexpected Netdata log directory')
        trimmed = 0
        for name in ['collector', 'daemon', 'debug', 'access', 'health', 'aclk']:
            if trim(LOGS / (name + '.log')):
                trimmed += 1
        (CONTROL / 'netdata-rotation-status.json').write_text(json.dumps({
            'last_success_epoch': time.time(), 'trimmed_logs': trimmed,
            'threshold_bytes': LIMIT, 'retained_samples_per_log': 3,
        }) + '\n')
        (CONTROL / 'netdata-rotation-notified').unlink(missing_ok=True)
        print('Netdata log retention checked; live file descriptors preserved.')
        return 0
    except (OSError, RuntimeError):
        storage_guard.MARKER = CONTROL / 'netdata-rotation-notified'
        storage_guard.notify_once('Mini Netdata log retention failed; inspect maintenance status before logs grow further.')
        return 1


if __name__ == '__main__':
    sys.exit(main())
