#!/usr/bin/python3
"""One-time, idle-runner migration. Never outputs existing configuration values."""
import datetime
import hashlib
import json
import os
import pathlib
import plistlib
import shutil
import stat
import subprocess
import sys

from storage_guard import ROOT, check

CONTROL = pathlib.Path('/Users/darcy/partyline-ci-control')
RUNNER = pathlib.Path('/Users/darcy/actions-runner')
PLIST = pathlib.Path('/Users/darcy/Library/LaunchAgents/actions.runner.partyline-sh-partyline.mac-mini.plist')
LABEL = 'actions.runner.partyline-sh-partyline.mac-mini'
DOMAIN = 'gui/' + str(os.getuid())
BACKUP = CONTROL / ('rollback-' + datetime.datetime.now().strftime('%Y%m%dT%H%M%S'))


def run(args, timeout=120):
    p = subprocess.run(args, capture_output=True, timeout=timeout)
    if p.returncode:
        raise RuntimeError('operation failed with exit code %d' % p.returncode)
    return p.stdout


def idle():
    if subprocess.run(['/usr/bin/pgrep', '-x', 'Runner.Worker'], capture_output=True).returncode != 1:
        raise RuntimeError('runner worker present or worker status unavailable')


def digest_tree(root):
    digest = hashlib.sha256()
    for base, dirs, files in os.walk(root, followlinks=False):
        dirs.sort()
        for name in sorted(dirs + files):
            p = pathlib.Path(base) / name
            mode = p.lstat().st_mode
            digest.update(str(p.relative_to(root)).encode())
            digest.update(str(stat.S_IFMT(mode)).encode())
            if p.is_symlink():
                digest.update(os.readlink(p).encode())
            elif stat.S_ISREG(mode):
                with p.open('rb') as source:
                    for block in iter(lambda: source.read(1024 * 1024), b''):
                        digest.update(block)
    return digest.hexdigest()


def status(phase, **extra):
    data = {'phase': phase, 'backup': str(BACKUP), **extra}
    p = CONTROL / 'install-status.json'
    p.write_text(json.dumps(data, indent=2) + '\n')
    p.chmod(0o600)
    print(phase, flush=True)


try:
    check()
    idle()
    if ROOT.exists() or ROOT.is_symlink():
        raise RuntimeError('CI directory already exists; refusing to reuse it')
    settings = json.loads((RUNNER / '.runner').read_text(encoding='utf-8-sig'))
    agent = plistlib.loads(PLIST.read_bytes())
    if settings.get('workFolder') != '_work' or agent.get('Label') != LABEL:
        raise RuntimeError('runner configuration differs from inspected state')
    BACKUP.mkdir(mode=0o700)
    for p in [RUNNER / '.runner', RUNNER / '.env', PLIST]:
        shutil.copy2(p, BACKUP / p.name)
        (BACKUP / p.name).chmod(0o600)
    status('stopping-idle-runner')
    run(['/bin/launchctl', 'bootout', DOMAIN, str(PLIST)])
    idle()
    if subprocess.run(['/usr/bin/pgrep', '-x', 'Runner.Listener'], capture_output=True).returncode != 1:
        raise RuntimeError('runner listener did not stop; no data moved')
    ROOT.mkdir(mode=0o700)
    for name in ['cache', 'tmp', 'logs', 'go']:
        (ROOT / name).mkdir(mode=0o700)
    (ROOT / '.managed-by-partyline-ci').write_text('CI storage only; preserve all other HDD folders.\n')
    status('copying-work-data')
    old_work = RUNNER / '_work'
    run(['/usr/bin/ditto', str(old_work), str(ROOT / 'work')], timeout=1800)
    status('verifying-work-data')
    if digest_tree(old_work) != digest_tree(ROOT / 'work'):
        raise RuntimeError('work copy did not verify; original retained, runner stopped')
    old_work.rename(RUNNER / ('_work.before-' + BACKUP.name))
    old_work.symlink_to(ROOT / 'work', target_is_directory=True)
    # Diagnostics and Actions' temporary/tool directories now remain on the HDD.
    diag = RUNNER / '_diag'
    if diag.is_symlink():
        raise RuntimeError('unexpected diagnostics symlink')
    if diag.exists():
        run(['/usr/bin/ditto', str(diag), str(ROOT / 'logs/diagnostics')], timeout=300)
        if digest_tree(diag) != digest_tree(ROOT / 'logs/diagnostics'):
            raise RuntimeError('diagnostics copy did not verify')
        diag.rename(RUNNER / ('_diag.before-' + BACKUP.name))
    else:
        (ROOT / 'logs/diagnostics').mkdir()
    diag.symlink_to(ROOT / 'logs/diagnostics', target_is_directory=True)
    settings['workFolder'] = str(ROOT / 'work')
    (RUNNER / '.runner').write_text(json.dumps(settings, indent=2) + '\n')
    (RUNNER / '.runner').chmod(0o600)
    old_lines = (RUNNER / '.env').read_text().splitlines()
    additions = {}
    for line in (CONTROL / 'runner.env.example').read_text().splitlines():
        if line and not line.startswith('#'):
            key, value = line.split('=', 1)
            additions[key] = value
            if value.startswith(str(ROOT)):
                pathlib.Path(value).mkdir(parents=True, exist_ok=True)
    kept = [line for line in old_lines if line.partition('=')[0] not in additions]
    (RUNNER / '.env').write_text('\n'.join(kept + [key + '=' + value for key, value in additions.items()]) + '\n')
    (RUNNER / '.env').chmod(0o600)
    agent['ProgramArguments'] = ['/usr/bin/python3', str(CONTROL / 'storage_guard.py'), '--launch']
    agent['KeepAlive'] = True
    agent['ThrottleInterval'] = 60
    agent['StandardOutPath'] = '/dev/null'
    agent['StandardErrorPath'] = '/dev/null'
    PLIST.write_bytes(plistlib.dumps(agent))
    for name in ['storage_guard.py', 'job-started.sh', 'cleanup.py']:
        (CONTROL / name).chmod(0o700)
    cleanup = PLIST.parent / 'com.partyline.ci-cleanup.plist'
    if cleanup.exists():
        raise RuntimeError('cleanup LaunchAgent already exists')
    shutil.copy2(CONTROL / 'cleanup-launchagent.plist', cleanup)
    check()
    run(['/usr/bin/python3', str(CONTROL / 'storage_guard.py')])
    status('starting-external-work-runner')
    run(['/bin/launchctl', 'bootstrap', DOMAIN, str(PLIST)])
    run(['/bin/launchctl', 'bootstrap', DOMAIN, str(cleanup)])
    status('installed', work_digest=digest_tree(ROOT / 'work'))
except Exception as error:
    reason = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    status('stopped', reason=reason)
    print('Migration stopped; originals and configuration backups retained. No monolith services changed.', flush=True)
    sys.exit(1)
