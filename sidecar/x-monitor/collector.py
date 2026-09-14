#!/usr/bin/env python3
"""One bounded, read-only request per subprocess; JSON on stdin/stdout."""
import argparse
import hashlib
import asyncio
import json
import os
import sys
from contextlib import aclosing
from pathlib import Path
from adapter import event, primary_tweet_ids, raw_media_order, user, username

os.environ["TWS_TELEMETRY"] = "0"
os.environ["TWS_HTTP_BACKEND"] = "curl"

ROOT = Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser()
parser.add_argument("--storage-dir", default=str(ROOT / "storage" / "x"))
STORAGE = Path(parser.parse_args().storage_dir)


class Failure(Exception):
    pass


async def collect(request):
    operation = request.get("operation")
    if operation not in {"lookup", "search", "timeline"}:
        raise Failure("invalid_operation")
    database = STORAGE / "accounts.db"
    session = STORAGE / "session.json"
    if not database.exists() and not session.exists():
        raise Failure("login_required")
    from twscrape import API
    from twscrape.logger import logger
    from twscrape import models
    from twscrape.models import parse_tweets
    def reject_parse_dump(*args, **kwargs):
        raise Failure('unsupported_response')
    models._write_dump = reject_parse_dump
    logger.remove()  # third-party exceptions/logs may include request credentials
    STORAGE.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(STORAGE, 0o700)
    api = API(str(database), raise_when_no_account=True, wait_timeout=0, proxy=request.get("proxyURL") or None)
    if session.exists():
        if session.stat().st_mode & 0o077:
            raise Failure("insecure_session_permissions")
        data = json.loads(session.read_text())
        cookies = {c["name"]: c["value"] for c in data.get("cookies", [])
                   if c.get("name") in {"auth_token", "ct0"} and c.get("domain", "").lstrip(".") in {"x.com", "twitter.com"}}
        if set(cookies) != {"auth_token", "ct0"} or any(not value or any(c in value for c in "\r\n;") for value in cookies.values()):
            raise Failure("invalid_session")
        digest = hashlib.sha256(json.dumps(cookies, sort_keys=True).encode()).hexdigest()
        marker = STORAGE / "session-imported.json"
        imported = json.loads(marker.read_text()) if marker.exists() else {}
        if imported.get("digest") != digest:
            await api.pool.add_account_cookies("browser_session", "; ".join(f"{key}={value}" for key, value in cookies.items()))
            marker.write_text(json.dumps({"digest": digest}))
            os.chmod(marker, 0o600)
    if database.exists():
        os.chmod(database, 0o600)
    if operation == "lookup":
        result = await api.user_by_login(username(request.get("query", "")))
        if result is None:
            raise Failure("user_unavailable")
        return {"user": user(result.dict())}
    limit = max(1, min(int(request.get("limit", 100)), 500))
    if operation == "search":
        query = request.get("query", "").strip()
        if not query or len(query) > 100:
            raise Failure("invalid_query")
        users = []
        async with aclosing(api.search_user(query, limit=min(limit, 20))) as items:
            async for item in items:
                users.append(user(item.dict()))
        return {"users": users}
    uid = str(request.get("userId", ""))
    if not uid.isdecimal():
        raise Failure("invalid_user_id")
    events, seen, page_count = [], set(), 0
    async with aclosing(api.user_tweets_and_replies_raw(int(uid), limit=limit)) as pages:
        async for response in pages:
            payload = response.json()
            page_count += 1
            if payload.get("errors"):
                raise Failure("timeline_unavailable")
            orders = raw_media_order(payload)
            primary = primary_tweet_ids(payload)
            for tweet in parse_tweets(payload):
                # Raw pages include other authors' conversation/quoted records.
                if str(tweet.user.id) != uid or tweet.id_str in seen or tweet.id_str not in primary:
                    continue
                seen.add(tweet.id_str)
                events.append(event(tweet.dict(), orders))
    if page_count == 0:
        raise Failure("timeline_unavailable")
    events.sort(key=lambda e: (e["time"], int(e["id"])))
    return {"events": events, "limit": limit, "coverageComplete": False,
            "coverageNote": "bounded timeline scan; BOT must validate overlap before advancing its cursor"}


def main():
    try:
        raw = sys.stdin.buffer.read(16385)
        if len(raw) > 16384:
            raise Failure("request_too_large")
        request = json.loads(raw)
        result = asyncio.run(asyncio.wait_for(collect(request), timeout=90))
        output = json.dumps({"ok": True, "data": result}, ensure_ascii=False)
        if len(output.encode()) > 8 * 1024 * 1024:
            raise Failure("response_too_large")
        print(output)
    except Exception as error:
        # Never return str(error): transport exceptions can contain signed URLs.
        code = str(error) if isinstance(error, Failure) else "timeout" if isinstance(error, TimeoutError) else "account_unavailable" if type(error).__name__ == "NoAccountError" else "invalid_request" if isinstance(error, (ValueError, TypeError, KeyError)) else "collection_failed"
        print(json.dumps({"ok": False, "error": {"code": code}}))
        sys.exit(1)


if __name__ == "__main__":
    main()
