#!/usr/bin/python3
"""Remove only idle, reproducible cache/temp trees older than 14 days."""
import fcntl
import os
import pathlib
import subprocess
import time

from storage_guard import ROOT, check


def prune():
    check(reserves=False)
    lock = (ROOT / '.cleanup-lock').open('a')
    try:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print('Cleanup already running; skipped.')
            return
        worker = subprocess.run(['/usr/bin/pgrep', '-x', 'Runner.Worker'], capture_output=True)
        if worker.returncode != 1:
            print('Cleanup skipped: runner worker present or status unavailable.')
            return
        cutoff = time.time() - 14 * 86400
        removed = 0
        # Never walk or prune runner workspaces, credentials or signed artifacts.
        for relative in ('cache', 'tmp', 'work/_temp'):
            root = ROOT / relative
            if root.resolve() != root:
                raise RuntimeError('cleanup root contains a symbolic link')
            if not root.exists():
                continue
            for base, dirs, files in os.walk(root, topdown=False, followlinks=False):
                for name in files:
                    path = pathlib.Path(base) / name
                    if path.is_symlink() or not path.is_file() or path.stat().st_mtime >= cutoff:
                        continue
                    path.unlink()
                    removed += 1
                for name in dirs:
                    path = pathlib.Path(base) / name
                    if not path.is_symlink():
                        try:
                            path.rmdir()  # Empty directories only; no recursive deletion.
                        except OSError:
                            pass
        print('Removed %d old cache/temp files; checkouts and artifacts retained.' % removed)
    finally:
        lock.close()


def main():
    # Nothing is removed while a job runs. A later scheduled run retries naturally.
    prune()


if __name__ == '__main__':
    main()
