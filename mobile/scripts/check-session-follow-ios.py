#!/usr/bin/env python3
"""Check fresh loading, live output, refresh feedback and reading older terminal rows on an explicit iOS simulator."""
import argparse
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--device', required=True, help='Explicit booted simulator UUID; never a physical phone')
parser.add_argument('--port', type=int, default=9045)
parser.add_argument('--maestro', default='maestro', help='Path to the installed Maestro executable')
args = parser.parse_args()
if not re.fullmatch(r'[A-Fa-f0-9-]{36}', args.device) or not 1 <= args.port <= 65535:
    parser.error('Pass a simulator UUID and valid Metro port.')
mobile = Path(__file__).resolve().parents[1]
target = mobile / 'src/preview/target.json'
original = target.read_bytes()
origin = f'http://localhost:{args.port}'
with urllib.request.urlopen(origin + '/status', timeout=5) as response:
    assert b'packager-status:running' in response.read(), 'Start screenshot Metro first.'
try:
    target.write_text('{"screen":"terminal-check"}\n')
    # Prime Expo with its real process argument; Maestro launch arguments use a different convention.
    subprocess.run(['xcrun', 'simctl', 'launch', '--terminate-running-process', args.device,
                    'sh.partyline.mobile', '--initialUrl',
                    origin + '?__expo_disable_fab=1&__expo_disable_onboarding=1'], check=True)
    flow = (mobile / 'e2e/session-follow.yml').read_text().replace('http://localhost:9045', origin)
    with tempfile.TemporaryDirectory(prefix='partyline-session-follow-') as temporary:
        path = Path(temporary) / 'session-follow.yml'
        path.write_text(flow)
        subprocess.run([args.maestro, '--device', args.device, 'test', str(path)], check=True)
finally:
    target.write_bytes(original)
