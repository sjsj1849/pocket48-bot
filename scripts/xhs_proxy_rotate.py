#!/usr/bin/env python3
"""Rotate mihomo-xhs outbound when Xiaohongshu hits IP risk.

Uses Clash/mihomo REST API:
  GET  /proxies/XHS-Auto  -> candidate pool + current
  PUT  /proxies/GLOBAL    -> pick next leaf (or force XHS-Auto)

Exit codes:
  0 success (switched or already ok)
  1 hard failure
  2 no API / no candidates
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

API = os.environ.get("MIHOMO_XHS_API", "http://127.0.0.1:19090")
STATE = os.environ.get("XHS_PROXY_STATE", "/root/pocket48-bot/storage/xhs-proxy-state.json")
GROUP = os.environ.get("MIHOMO_XHS_POOL", "XHS-Auto")
SELECTOR = os.environ.get("MIHOMO_XHS_SELECTOR", "GLOBAL")


def api(path: str, method: str = "GET", body: dict | None = None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        API.rstrip("/") + path,
        data=data,
        method=method,
        headers={"Content-Type": "application/json"} if body is not None else {},
    )
    with urllib.request.urlopen(req, timeout=8) as resp:
        raw = resp.read()
        return json.loads(raw.decode() or "{}") if raw else {}


def load_state() -> dict:
    try:
        with open(STATE, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return {"used": [], "last": ""}


def save_state(st: dict) -> None:
    os.makedirs(os.path.dirname(STATE) or ".", exist_ok=True)
    tmp = STATE + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(st, f, ensure_ascii=False, indent=2)
    os.replace(tmp, STATE)


def candidates() -> tuple[list[str], str]:
    # Prefer pool group; fall back to GLOBAL all leaves
    try:
        d = api(f"/proxies/{GROUP}")
        all_nodes = [x for x in d.get("all") or [] if x not in ("DIRECT", "REJECT")]
        now = str(d.get("now") or "")
        if all_nodes:
            return all_nodes, now
    except Exception:
        pass
    d = api(f"/proxies/{SELECTOR}")
    all_nodes = [x for x in d.get("all") or [] if x not in ("DIRECT", "REJECT", "GLOBAL")]
    return all_nodes, str(d.get("now") or "")


def pick_next(pool: list[str], current: str, used: list[str]) -> str:
    # Prefer unused nodes first, then wrap
    ordered = [n for n in pool if n not in used] or list(pool)
    if not ordered:
        raise RuntimeError("empty pool")
    if current in ordered and len(ordered) > 1:
        i = ordered.index(current)
        return ordered[(i + 1) % len(ordered)]
    # skip current if present in pool
    for n in ordered:
        if n != current:
            return n
    return ordered[0]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--reason", default="")
    ap.add_argument("--reset-used", action="store_true")
    ap.add_argument("--to", default="", help="force select this node name")
    args = ap.parse_args()

    try:
        pool, now = candidates()
    except Exception as e:
        print(f"api_fail: {e}", file=sys.stderr)
        return 2
    if not pool:
        print("no_candidates", file=sys.stderr)
        return 2

    st = load_state()
    if args.reset_used:
        st["used"] = []
    used = list(st.get("used") or [])
    target = args.to.strip() or pick_next(pool, now or str(st.get("last") or ""), used)

    try:
        # Select concrete leaf on GLOBAL so traffic switches immediately
        api(f"/proxies/{SELECTOR}", method="PUT", body={"name": target})
    except urllib.error.HTTPError as e:
        # Some controllers want the pool group; try that then GLOBAL=XHS-Auto
        try:
            if target in pool:
                api(f"/proxies/{GROUP}", method="PUT", body={"name": target})
            api(f"/proxies/{SELECTOR}", method="PUT", body={"name": GROUP})
        except Exception as e2:
            print(f"switch_fail: {e} / {e2}", file=sys.stderr)
            return 1
    except Exception as e:
        print(f"switch_fail: {e}", file=sys.stderr)
        return 1

    if target not in used:
        used.append(target)
    # keep used list bounded
    if len(used) >= max(8, len(pool)):
        used = used[-max(3, len(pool) // 3) :]
    st["used"] = used
    st["last"] = target
    st["reason"] = args.reason
    save_state(st)
    print(json.dumps({"ok": True, "from": now, "to": target, "pool": len(pool), "reason": args.reason}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
