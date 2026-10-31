#!/usr/bin/env python3
"""Capture an existing debug app from an isolated screenshot Metro server; never clears app data."""
import argparse
import json
from pathlib import Path
import re
import subprocess
import time
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--device', required=True, help='Explicit booted simulator UUID')
parser.add_argument('--port', type=int, default=8084)
parser.add_argument('--settle-seconds', type=float, default=12, help='Wait for cold-start bundling and rendering before each capture')
parser.add_argument('--screens', nargs='+', choices=['sessions', 'messages', 'terminal', 'loops', 'chat', 'computers'], default=['sessions', 'messages', 'terminal', 'loops', 'chat', 'computers'])
args = parser.parse_args()
if not re.fullmatch(r'[A-Fa-f0-9-]{36}', args.device):
    parser.error('Pass an explicit iOS simulator UUID, not a physical-device ID.')
mobile = Path(__file__).resolve().parents[1]
target = mobile / 'src/preview/target.json'
original = target.read_bytes()
output = mobile / 'artifacts/store-assets/raw'
output.mkdir(parents=True, exist_ok=True)
base = f'http://localhost:{args.port}'
with urllib.request.urlopen(base + '/status', timeout=5) as response:
    assert b'packager-status:running' in response.read(), 'Start screenshot Metro first.'
try:
    subprocess.run(['xcrun', 'simctl', 'status_bar', args.device, 'override', '--time', '9:41', '--dataNetwork', 'wifi', '--wifiMode', 'active', '--wifiBars', '3', '--batteryState', 'discharging', '--batteryLevel', '100'], check=True)
    for screen in args.screens:
        target.write_text(json.dumps({'screen': screen}) + '\n')
        # Expo 58 serves HTML at /reload. Relaunch the app so fixtures and effects
        # really start fresh, rather than relying on Fast Refresh to change routes.
        subprocess.run(['xcrun', 'simctl', 'launch', '--terminate-running-process', args.device,
                        'sh.partyline.mobile', '--initialUrl',
                        base + '?__expo_disable_fab=1&__expo_disable_onboarding=1'],
                       check=True, capture_output=True)
        time.sleep(args.settle_seconds)
        subprocess.run(['xcrun', 'simctl', 'io', args.device, 'screenshot', str(output / f'{screen}.png')], check=True)
        print('Captured native ' + screen, flush=True)
finally:
    target.write_bytes(original)
print('Inspect the raw images, then run npm run store:assets to compose them.', flush=True)
