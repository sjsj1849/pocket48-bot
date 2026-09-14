#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
python3 -m venv "$ROOT_DIR/sidecar/instagram-monitor/.venv"
"$ROOT_DIR/sidecar/instagram-monitor/.venv/bin/python" -m pip install -r "$ROOT_DIR/sidecar/instagram-monitor/requirements.lock"
