"""Read-only Instaloader bridge. Secrets arrive on stdin and stay in private JSON."""
import argparse
import contextlib
import fcntl
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import time

import instaloader


class Failure(Exception):
    def __init__(self, code):
        self.code = code


class BoundedRateController(instaloader.RateController):
    def sleep(self, secs):
        # Let the Go scheduler back off instead of blocking a worker for minutes.
        if secs > 10:
            raise Failure("rate_limit")
        super().sleep(secs)


def username(raw):
    if not re.fullmatch(r"[A-Za-z0-9._]{1,30}", raw or ""):
        raise Failure("user_unavailable")
    return raw.lower()


def login_identifier(raw):
    raw = (raw or "").strip()
    if len(raw) > 254 or not re.fullmatch(r"[A-Za-z0-9._@+\-]+", raw):
        raise Failure("bad_credentials")
    return raw


def parse_cookies(raw):
    allowed = {"sessionid", "csrftoken", "ds_user_id", "mid", "ig_did", "rur", "datr"}
    try:
        if raw.lstrip().startswith(("[", "{")):
            source = json.loads(raw)
            if isinstance(source, dict) and "cookies" in source:
                source = source["cookies"]
            if isinstance(source, list):
                cookies = {x["name"]: x["value"] for x in source
                           if x.get("domain", "").lstrip(".") in ("instagram.com", "www.instagram.com")}
            elif isinstance(source, dict):
                cookies = source
            else:
                raise ValueError()
        else:
            cookies = dict(x.strip().split("=", 1) for x in raw.split(";") if "=" in x)
        cookies = {k: v for k, v in cookies.items() if k in allowed and isinstance(v, str)}
        if not cookies.get("sessionid") or not cookies.get("csrftoken"):
            raise ValueError()
        if any(len(v) > 8192 or "\n" in v or "\r" in v for v in cookies.values()):
            raise ValueError()
        return cookies
    except (ValueError, TypeError, KeyError, AttributeError):
        raise Failure("invalid_session")


def write_private(path, data):
    fd, temp = tempfile.mkstemp(dir=path.parent, prefix=".session-")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(data, f)
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def write_session(path, context, login):
    write_private(path, {"username": login, "cookies": context.save_session()})


def profile_data(profile):
    return {"id": str(profile.userid), "username": profile.username,
            "name": profile.full_name or profile.username, "avatar": profile.profile_pic_url,
            "protected": profile.is_private}


def post_data(post, author, forced_kind=None):
    kind = forced_kind or ("reel" if post._node.get("product_type") == "clips" else "post")
    media = []
    if post.typename == "GraphSidecar":
        nodes = post.get_sidecar_nodes()
    else:
        nodes = [post]
    for node in nodes:
        cover = node.display_url if hasattr(node, "display_url") else node.url
        if node.is_video:
            video = node.video_url
            media.append({"kind": "video", "cover": cover,
                          "variants": [{"url": video, "bitrate": 1}] if video else []})
        else:
            media.append({"kind": "image", "url": cover})
    return {"id": str(post.mediaid), "kind": kind, "author": author,
            "body": post.caption or "", "url": "https://www.instagram.com/p/" + post.shortcode + "/",
            "time": int(post.date_utc.timestamp() * 1000), "media": media, "mediaOrderKnown": True}


def story_data(item, author):
    media = {"kind": "image", "url": item.url}
    if item.is_video:
        media = {"kind": "video", "cover": item.url,
                 "variants": [{"url": item.video_url, "bitrate": 1}] if item.video_url else []}
    return {"id": str(item.mediaid), "kind": "story", "author": author, "body": "",
            "url": f'https://www.instagram.com/stories/{author["username"]}/{item.mediaid}/',
            "time": int(item.date_utc.timestamp() * 1000), "media": [media], "mediaOrderKnown": True}


def scan_posts(iterator, author, limit, since, forced=None):
    events = []
    old = 0
    for i, post in enumerate(iterator):
        if i >= limit:
            # An incomplete scan must not establish a false success or advance cursors.
            if since and old < 4:
                raise Failure("scan_incomplete")
            break
        stamp = int(post.date_utc.timestamp() * 1000)
        if since and stamp < since:
            old += 1
            # Profile may pin up to three old posts ahead of recent ones.
            if old >= 4:
                break
            continue
        old = 0
        events.append(post_data(post, author, forced))
    return events


def execute(request, directory):
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    path = directory / "session.json"
    with open(directory / "session.lock", "a") as lock:
        os.chmod(lock.name, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX)
        operation = request.get("operation")
        if operation == "session_clear":
            path.unlink(missing_ok=True)
            (directory / "pending-2fa.json").unlink(missing_ok=True)
            return {"sessionConfigured": False}
        loader = instaloader.Instaloader(quiet=True, sleep=False, max_connection_attempts=1,
                                       request_timeout=20, rate_controller=BoundedRateController)
        proxy = request.get("proxyURL")
        login = ""
        if proxy:
            os.environ["HTTP_PROXY"] = os.environ["HTTPS_PROXY"] = proxy
        if operation in ("session_login", "session_2fa"):
            pending_path = directory / "pending-2fa.json"
            if operation == "session_login":
                name = login_identifier(request.get("username"))
                password = request.get("password", "")
                if not isinstance(password, str) or not password or len(password) > 1024:
                    raise Failure("bad_credentials")
                try:
                    loader.login(name, password)
                except instaloader.exceptions.TwoFactorAuthRequiredException:
                    session, user, identifier = loader.context.two_factor_auth_pending
                    write_private(pending_path, {"username": user, "cookies": session.cookies.get_dict(), "identifier": identifier, "expires": time.time() + 600})
                    raise Failure("two_factor_required")
            else:
                if not pending_path.exists():
                    raise Failure("two_factor_expired")
                pending = json.loads(pending_path.read_text())
                if pending.get("expires", 0) < time.time():
                    pending_path.unlink(missing_ok=True)
                    raise Failure("two_factor_expired")
                code = request.get("code", "")
                if not re.fullmatch(r"[0-9]{6,8}", code):
                    raise Failure("bad_credentials")
                loader.context.load_session(login_identifier(pending["username"]), pending["cookies"])
                if proxy:
                    loader.context._session.proxies.update({"http": proxy, "https": proxy})
                loader.context.two_factor_auth_pending = (loader.context._session, pending["username"], pending["identifier"])
                loader.two_factor_login(code)
            login = loader.context.username
            if "@" in login or login.startswith("+") or login.isdigit():
                login = loader.test_login()
                if not login:
                    raise Failure("login_blocked")
            login = username(login)
            loader.context.username = login
            write_session(path, loader.context, login)
            pending_path.unlink(missing_ok=True)
            return {"username": login, "sessionConfigured": True}
        if operation == "session_import":
            loader.context.load_session("imported", parse_cookies(request.get("cookies", "")))
        elif path.exists():
            if path.stat().st_mode & 0o077:
                raise Failure("insecure_session_permissions")
            session = json.loads(path.read_text())
            login = username(session["username"])
            loader.context.load_session(login, session["cookies"])
        if proxy:
            loader.context._session.proxies.update({"http": proxy, "https": proxy})
        if operation in ("session_import", "session_check"):
            if operation == "session_check" and not login:
                raise Failure("login_required")
            verified = loader.test_login()
            if not verified:
                raise Failure("login_required")
            login = username(verified)
            loader.context.username = login
            write_session(path, loader.context, login)
            return {"username": login, "sessionConfigured": True}
        if operation not in ("lookup", "timeline"):
            raise Failure("invalid_request")
        name = username(request.get("query") if operation == "lookup" else request.get("username"))
        profile = instaloader.Profile.from_username(loader.context, name)
        author = profile_data(profile)
        if operation == "lookup":
            result = {"user": author}
        else:
            if profile.is_private and not login:
                raise Failure("login_required")
            limit = min(max(int(request.get("limit", 100)), 1), 500)
            since = request.get("since") or {}
            events = []
            if request.get("posts", True):
                events.extend(scan_posts(profile.get_posts(), author, limit, max(int(since.get("post", 0)), 0)))
            if request.get("reels", True):
                events.extend(scan_posts(profile.get_reels(), author, limit, max(int(since.get("reel", 0)), 0), "reel"))
            if request.get("stories"):
                if not login:
                    raise Failure("login_required")
                for story in loader.get_stories(userids=[profile.userid]):
                    events.extend(story_data(item, author) for item in story.get_items())
            # Feed and Reels can expose the same media; canonicalize by media ID.
            dedup = {}
            for event in events:
                key = ("story" if event["kind"] == "story" else "post", event["id"])
                if key not in dedup or event["kind"] == "reel":
                    dedup[key] = event
            result = {"user": author, "events": list(dedup.values())}
        if login:
            write_session(path, loader.context, login)
        return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--storage-dir", required=True)
    args = parser.parse_args()
    try:
        request = json.loads(sys.stdin.read(128 * 1024))
        # Library errors occasionally print raw URLs: suppress all worker diagnostics.
        with contextlib.redirect_stdout(open(os.devnull, "w")), contextlib.redirect_stderr(open(os.devnull, "w")):
            result = execute(request, Path(args.storage_dir))
        output = {"ok": True, "data": result}
    except Failure as error:
        output = {"ok": False, "error": {"code": error.code}}
    except instaloader.exceptions.TooManyRequestsException:
        output = {"ok": False, "error": {"code": "rate_limit"}}
    except instaloader.exceptions.BadCredentialsException:
        output = {"ok": False, "error": {"code": "bad_credentials"}}
    except instaloader.exceptions.TwoFactorAuthRequiredException:
        output = {"ok": False, "error": {"code": "two_factor_required"}}
    except (instaloader.exceptions.LoginRequiredException, instaloader.exceptions.LoginException) as error:
        message = str(error).lower()
        code = "rate_limit" if "429" in message or "too many requests" in message else "checkpoint_required" if "checkpoint" in message else "bad_credentials" if "does not exist" in message else "login_blocked"
        output = {"ok": False, "error": {"code": code}}
    except instaloader.exceptions.ProfileNotExistsException:
        output = {"ok": False, "error": {"code": "user_unavailable"}}
    except instaloader.exceptions.ConnectionException as error:
        # Instaloader may wrap HTTP 429 as a generic ConnectionException.
        message = str(error).lower()
        code = "rate_limit" if "429" in message or "too many requests" in message else "login_required" if any(s in message for s in ("login_required", "login required", "challenge_required", "checkpoint_required")) else "account_unavailable"
        output = {"ok": False, "error": {"code": code}}
    except instaloader.exceptions.PrivateProfileNotFollowedException:
        output = {"ok": False, "error": {"code": "access_denied"}}
    except Exception:
        output = {"ok": False, "error": {"code": "account_unavailable"}}
    print(json.dumps(output, ensure_ascii=False))


if __name__ == "__main__":
    main()
