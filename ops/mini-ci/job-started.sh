#!/bin/bash
set -eu
exec /usr/bin/python3 "$(dirname "$0")/storage_guard.py"
