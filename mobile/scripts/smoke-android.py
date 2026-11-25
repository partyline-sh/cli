#!/usr/bin/env python3
"""Native smoke checks on an already booted, fresh emulator. Never clears app data."""
import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import time
import xml.etree.ElementTree as ET

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--serial', required=True, help='Explicit emulator-NNNN target')
parser.add_argument('--apk', type=Path, required=True)
parser.add_argument('--artifacts', type=Path, default=Path('artifacts/android-smoke'))
args = parser.parse_args()
if not re.fullmatch(r'emulator-\d+', args.serial):
    parser.error('This fixture smoke test only targets an Android emulator, never a physical phone.')
sdk = os.environ.get('ANDROID_HOME') or os.environ.get('ANDROID_SDK_ROOT') or str(Path.home() / 'Library/Android/sdk')
adb_binary = shutil.which('adb') or str(Path(sdk) / 'platform-tools/adb')
args.artifacts.mkdir(parents=True, exist_ok=True)

def adb(*parts):
    return subprocess.check_output([adb_binary, '-s', args.serial, *parts], timeout=30)

def nodes():
    adb('shell', 'uiautomator', 'dump', '/sdcard/partyline-smoke.xml')
    return list(ET.fromstring(adb('exec-out', 'cat', '/sdcard/partyline-smoke.xml')).iter('node'))

def contains(label):
    return any(label in n.get('text', '') or label in n.get('content-desc', '') for n in nodes())

def tap(label):
    for n in nodes():
        if n.get('text') == label or n.get('content-desc') == label:
            bounds = list(map(int, re.findall(r'\d+', n.get('bounds', ''))))
            if len(bounds) == 4:
                adb('shell', 'input', 'tap', str((bounds[0] + bounds[2]) // 2), str((bounds[1] + bounds[3]) // 2))
                time.sleep(.5)
                return
    raise AssertionError('Missing control: ' + label)

def screenshot(name):
    (args.artifacts / name).write_bytes(adb('exec-out', 'screencap', '-p'))

def launch():
    adb('shell', 'am', 'start', '-W', '-n', 'sh.partyline.mobile/.MainActivity')
    time.sleep(1)

adb('install', '-r', str(args.apk.resolve()))
launch()
tap('Sessions')
assert contains('No computer paired yet'), 'Use a fresh emulator without personal pairings; this script will not clear them.'
screenshot('sessions.png')
tap('Loops')
assert contains('No loops on your computers.')
tap('Chat')
assert contains('Pair a computer to see its chat room.')
tap('Computers')
assert contains('Scan a pairing code')
adb('shell', 'am', 'force-stop', 'sh.partyline.mobile')
launch()
assert contains('Scan a pairing code'), 'Selected tab was not restored after process restart.'
print('PASS native navigation and tab restoration', flush=True)
tap('Scan a pairing code')
assert contains('Pair a computer')
if contains('Allow the camera'):
    tap('Allow the camera')
    for n in nodes():
        if 'permission_allow_foreground_only_button' in n.get('resource-id', ''):
            b = list(map(int, re.findall(r'\d+', n.get('bounds', ''))))
            adb('shell', 'input', 'tap', str((b[0] + b[2]) // 2), str((b[1] + b[3]) // 2))
            break
    time.sleep(1)
permissions = adb('shell', 'dumpsys', 'package', 'sh.partyline.mobile').decode()
assert 'android.permission.CAMERA: granted=true' in permissions
screenshot('camera-permission.png')
tap('Pairing link')
adb('shell', 'input', 'text', 'not-a-code')
time.sleep(.5)
screenshot('pairing-keyboard.png')
# Pair must be reachable while the keyboard is still open.
tap('Pair')
assert contains('Scan or paste the pairing link'), 'Invalid input did not show readable guidance.'
assert contains('Scan again')
screenshot('pairing-error.png')
tap('Close')
assert contains('Scan a pairing code')
print('PASS camera permission, invalid pairing input, and cancel', flush=True)
print('Physical QR decoding and paired-session interaction are NOT exercised by this test.', flush=True)
