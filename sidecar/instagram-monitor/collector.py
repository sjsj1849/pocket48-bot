"""Read-only Instaloader bridge. Secrets arrive on stdin and stay in private JSON."""
import argparse
import base64
import copy
import contextlib
import datetime
import fcntl
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import time

import instaloader
import requests
from persistent_rate import PersistentLimiter, RateBlocked, SessionBlocked
from feed_v1 import MobileClient, user_posts_v1, user_reels_graphql, FeedError
from browser_transport import browser_transport
from guest_web import public_profile, public_posts, GuestWebError


class Failure(Exception):
    def __init__(self, code):
        self.code = code


MOBILE_BRIDGE_HEADERS = {
    'authorization', 'user-agent', 'x-ig-app-id', 'x-bloks-version-id',
    'x-ig-device-id', 'x-ig-android-id', 'x-ig-family-device-id', 'x-mid',
    'ig-u-rur', 'ig-u-ds-user-id', 'x-ig-user-id', 'x-ig-www-claim',
    'x-ig-app-locale', 'x-ig-device-locale', 'x-ig-mapped-locale',
    'x-ig-timezone-offset', 'x-ig-capabilities', 'x-ig-connection-type',
}


def decode_mobile_authorization(value, failure_code='invalid_session'):
    prefix = 'Bearer IGT:2:'
    try:
        if not isinstance(value, str) or not value.startswith(prefix) or len(value) > 4096:
            raise ValueError()
        if '\n' in value or '\r' in value:
            raise ValueError()
        encoded = value[len(prefix):]
        encoded += '=' * (-len(encoded) % 4)
        payload = json.loads(base64.b64decode(encoded, validate=True))
        user_id = str(payload.get('ds_user_id') or '')
        session_id = str(payload.get('sessionid') or '')
        if not user_id.isdigit() or not session_id or len(session_id) > 4096:
            raise ValueError()
        return {'ds_user_id': user_id, 'sessionid': session_id}
    except (ValueError, TypeError, KeyError, json.JSONDecodeError):
        raise Failure(failure_code) from None


def set_case_insensitive(mapping, name, value):
    existing = next((key for key in mapping if key.lower() == name.lower()), name)
    mapping[existing] = value


def apply_mobile_session(request, directory, mobile_path, proxy, limiter=None):
    if not mobile_path.exists() or mobile_path.stat().st_mode & 0o077:
        raise Failure('invalid_session')
    try:
        original = json.loads(mobile_path.read_text())
        old_headers = original['headers']
        old_authorization = next(
            value for name, value in old_headers.items() if name.lower() == 'authorization')
    except (OSError, ValueError, KeyError, StopIteration, TypeError):
        raise Failure('invalid_session') from None

    authorization = request.get('authorization')
    old_identity = decode_mobile_authorization(old_authorization)
    new_identity = decode_mobile_authorization(authorization, 'session_candidate_invalid')
    if old_identity['ds_user_id'] != new_identity['ds_user_id']:
        raise Failure('session_account_mismatch')

    supplied = request.get('headers') or {}
    if not isinstance(supplied, dict) or len(supplied) > 64:
        raise Failure('session_candidate_headers_invalid')
    candidate = copy.deepcopy(original)
    set_case_insensitive(candidate['headers'], 'Authorization', authorization)
    for name, value in supplied.items():
        if (not isinstance(name, str) or name.lower() not in MOBILE_BRIDGE_HEADERS
                or name.lower() == 'authorization' or not isinstance(value, str)
                or len(name) > 256 or len(value) > 16384 or '\n' in name + value or '\r' in name + value):
            raise Failure('session_candidate_headers_invalid')
        set_case_insensitive(candidate['headers'], name, value)
    # Validate entirely in memory. A failed candidate must never touch the live file.
    client = MobileClient(candidate, proxy_url=proxy)
    # Recovery is the one operation that must run *because* the account is in a
    # cooldown. Gating it would make a dead session unrecoverable: the collector
    # sleeps, the new token can never be verified, and the backoff keeps growing.
    previous_exempt = getattr(limiter, 'exempt', False)
    if limiter is not None:
        limiter.exempt = True
    try:
        result = execute_mobile({'operation': 'session_check'}, client, directory)
    finally:
        if limiter is not None:
            limiter.exempt = previous_exempt
    validated_identity = decode_mobile_authorization(
        next(value for name, value in candidate['headers'].items()
             if name.lower() == 'authorization'))
    if validated_identity['ds_user_id'] != old_identity['ds_user_id']:
        raise Failure('session_account_mismatch')

    backup_dir = directory / 'session-backups'
    backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(backup_dir, 0o700)
    backup = backup_dir / f'mobile-session.{int(time.time() * 1000)}.json'
    write_private(backup, original)
    write_private(mobile_path, candidate)
    result['sessionUpdated'] = True
    # The fresh token answered a real API call, so the account is reachable.
    # Release the backoff instead of leaving collection asleep until it expires.
    if limiter is not None:
        limiter.thaw()
    return result



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


def resolve_profile(context, name, directory=None):
    # web_profile_info can return 429 even with a valid session. Authenticated
    # search provides the stable ID; the Profile then loads metadata via GraphQL.
    if context.is_logged_in:
        metadata_path = directory / 'profile-metadata.json' if directory else None
        metadata = json.loads(metadata_path.read_text()) if metadata_path and metadata_path.exists() else {}
        saved = metadata.get(name) or {}
        if (0 <= time.time() - saved.get('at', 0) < 600
                and isinstance(saved.get('node'), dict)
                and str(saved['node'].get('username', '')).lower() == name):
            profile = instaloader.Profile(context, saved['node'])
            profile._has_full_metadata = True
            return profile

        def remember(profile):
            if metadata_path and isinstance(profile._node, dict):
                metadata[name] = {'at': time.time(), 'node': profile._node}
                write_private(metadata_path, metadata)
            return profile

        cache_path = directory / "profile-cache.json" if directory else None
        cache = json.loads(cache_path.read_text()) if cache_path and cache_path.exists() else {}
        cached_id = cache.get(name)
        if isinstance(cached_id, str) and cached_id.isdigit():
            profile = instaloader.Profile(context, {"id": cached_id, "username": name})
            profile._obtain_metadata()
            if str(profile.userid) != cached_id or profile.username.lower() != name:
                raise Failure("user_unavailable")
            return remember(profile)
        for profile in instaloader.TopSearchResults(context, name).get_profiles():
            if profile.username.lower() == name.lower():
                if cache_path:
                    cache[name] = str(profile.userid)
                    write_private(cache_path, cache)
                profile._obtain_metadata()
                return remember(profile)
        raise Failure("user_unavailable")
    return instaloader.Profile.from_username(context, name)


def profile_data(profile):
    return {"id": str(profile.userid), "username": profile.username,
            "name": profile.full_name or profile.username, "avatar": profile._node.get("profile_pic_url") or profile._node.get("profile_pic_url_hd") or "",
            "protected": profile.is_private}


def guest_profile(session, name):
    return public_profile(session, name)


def timestamp_ms(value):
    if value.tzinfo is None:
        value = value.replace(tzinfo=datetime.timezone.utc)
    return int(value.timestamp() * 1000)


def post_data(post, author, forced_kind=None):
    raw = post._node.get('iphone_struct')
    if isinstance(raw, dict):
        return raw_post_data(raw, author, forced_kind)
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
            "time": timestamp_ms(post.date_utc), "media": media, "mediaOrderKnown": True}


def raw_post_data(item, author, forced_kind=None):
    """Parse list payloads without lazily asking Instagram for media details."""
    kind = forced_kind or ('reel' if item.get('product_type') == 'clips' else 'post')
    media = []
    nodes = item.get('carousel_media') if item.get('media_type') == 8 else [item]
    if not nodes:
        raise Failure('scan_incomplete')
    for node in nodes:
        candidates = (node.get('image_versions2') or {}).get('candidates') or []
        images = [image for image in candidates if image.get('url')]
        cover = max(images, key=lambda image: int(image.get('width') or 0) * int(image.get('height') or 0))['url'] if images else ''
        if node.get('media_type') == 2:
            videos = [video for video in node.get('video_versions') or [] if video.get('url')]
            if not videos:
                raise Failure('scan_incomplete')
            media.append({'kind': 'video', 'cover': cover,
                          'durationMS': int(float(node.get('video_duration') or 0) * 1000),
                          'variants': [{'url': video['url'], 'bitrate': int(video.get('bitrate') or (int(video.get('width') or 0) * int(video.get('height') or 0)) or 1)} for video in videos]})
        else:
            if not cover:
                raise Failure('scan_incomplete')
            media.append({'kind': 'image', 'url': cover})
    code = str(item.get('code') or '')
    identifier = str(item.get('pk') or '')
    if not code or not identifier or not item.get('taken_at'):
        raise Failure('scan_incomplete')
    return {'id': identifier, 'kind': kind, 'author': author,
            'body': (item.get('caption') or {}).get('text') or '',
            'url': f'https://www.instagram.com/{"reel" if kind == "reel" else "p"}/{code}/',
            'time': int(item['taken_at']) * 1000, 'media': media, 'mediaOrderKnown': True}


def mobile_author(user):
    identifier = str(user.get('pk') or user.get('id') or '')
    handle = username(str(user.get('username') or ''))
    if not identifier.isdigit():
        raise Failure('user_unavailable')
    return {'id': identifier, 'username': handle,
            'name': user.get('full_name') or handle,
            'avatar': user.get('profile_pic_url_hd') or user.get('profile_pic_url') or '',
            'protected': bool(user.get('is_private'))}


def remember_mobile_author(directory, name, identifier):
    """Remember a resolved id so later polls can skip usernameinfo entirely."""
    path = directory / 'profile-cache.json'
    try:
        cached = json.loads(path.read_text()) if path.exists() else {}
    except (OSError, ValueError):
        cached = {}
    if not isinstance(cached, dict) or cached.get(name) == identifier:
        return
    cached[name] = identifier
    try:
        write_private(path, cached)
    except OSError:
        pass


def adopt_mobile_author(author, user):
    """Fill an id-only author from the user object the feed already returned."""
    if not isinstance(user, dict):
        return
    handle = str(user.get('username') or '')
    if handle:
        try:
            author['username'] = username(handle)
        except Failure:
            pass
    if user.get('full_name'):
        author['name'] = user['full_name']
    avatar = user.get('profile_pic_url_hd') or user.get('profile_pic_url')
    if avatar:
        author['avatar'] = avatar
    if 'is_private' in user:
        author['protected'] = bool(user['is_private'])


def cached_mobile_author(directory, name):
    """Build the author from the cached id, spending no request at all.

    Every timeline poll used to call /api/v1/users/<name>/usernameinfo/ only to
    turn a name into an id it had already cached. That endpoint is the one that
    answers 429, and each 429 freezes the whole account, so the redundant call
    was manufacturing the very cooldown that then blocked collection. Display
    fields are filled in from the feed items themselves (see scan_mobile_items).
    """
    try:
        cached = json.loads((directory / 'profile-cache.json').read_text())
    except (OSError, ValueError):
        return None
    identifier = cached.get(name) if isinstance(cached, dict) else None
    if not (isinstance(identifier, str) and identifier.isdigit()):
        return None
    return {'id': identifier, 'username': name, 'name': name,
            'avatar': '', 'protected': False}


def scan_mobile_items(iterator, author, limit, since, forced=None):
    events = []
    old = 0
    for i, item in enumerate(iterator):
        if i >= limit:
            if since and old < 4:
                raise Failure('scan_incomplete')
            break
        owner_user = item.get('user') or {}
        owner = str(owner_user.get('pk') or '')
        collaborators = [str(user.get('pk')) for user in item.get('coauthor_producers') or []]
        if owner and owner != author['id'] and author['id'] not in collaborators:
            raise Failure('user_unavailable')
        # A cached author only knows the id; the feed item carries the rest, so
        # avatar/display name still look right without a usernameinfo call.
        if owner == author['id']:
            adopt_mobile_author(author, owner_user)
        stamp = int(item.get('taken_at') or 0) * 1000
        if since and stamp < since:
            old += 1
            if old >= 4:
                break
            continue
        old = 0
        events.append(raw_post_data(item, author, forced))
    return events


def mobile_story_data(item, author):
    prepared = {**item, 'code': item.get('code') or str(item.get('pk') or '')}
    event = raw_post_data(prepared, author, 'story')
    event['url'] = f'https://www.instagram.com/stories/{author["username"]}/{event["id"]}/'
    return event


def mobile_post_by_code(client, code, hint_username, directory):
    """按短码定位一条内容，返回带完整 media 的原始节点。

    ★ 为什么需要这一层（实测 2026-10-09）：
    Instagram 已下线 media/shortcode/<code>/info/（返回 404 + HTML），
    而 /p/<code>/ 链接里**没有作者用户名**，数字 media id 也无从得知。
    于是只剩一条路：先有一个候选作者，再去他的列表里按短码精确匹配。

    候选来源按「代价从低到高」：
      1. 调用方给的用户名（Story 链接能直接带出来）→ 一次 usernameinfo；
      2. profile-cache.json 里已知的作者（都是已订阅、已解析过的）→ 无需额外查询。

    每个候选内部都是「命中即停」，所以常见情形只查一个作者。
    """
    if not code:
        raise Failure('user_unavailable')
    candidates = []
    if hint_username:
        candidates.append(str(hint_username))
    try:
        cached = json.loads((directory / 'profile-cache.json').read_text())
        if isinstance(cached, dict):
            candidates.extend(str(name) for name in cached)
    except (OSError, ValueError):
        pass
    seen = set()
    for name in candidates:
        if not name or name in seen:
            continue
        seen.add(name)
        try:
            user = mobile_author(client.user_info(name))
            for iterator in (client.user_posts, client.user_clips):
                for item in iterator(user['id']):
                    if str(item.get('code') or '') != code:
                        continue
                    pk = str(item.get('pk') or '')
                    if not pk.isdigit():
                        continue
                    # 列表项对轮播帖不含顶层 video_versions，必须再取一次详情。
                    return client.media_info(pk)
        except FeedError:
            continue
    raise Failure('user_unavailable')


def execute_mobile(request, client, directory):
    operation = request.get('operation')
    if operation == 'session_check':
        account = mobile_author(client.current_user())
        return {'username': account['username'], 'sessionConfigured': True}
    if operation == 'post_detail':
        # 链接提取用：只取单条，不翻整条时间线。
        # mediaId 优先（内部调用）；分享链接只有短码，而
        # media/shortcode/<code>/info/ 已被 Instagram 下线（实测 404），
        # 所以必须先有一个 user_id，再在该作者的列表里按短码定位。
        media_id = str(request.get('mediaId') or '')
        if media_id.isdigit():
            item = client.media_info(media_id)
        else:
            item = mobile_post_by_code(client, str(request.get('code') or ''),
                                        request.get('username'), directory)
        # 单条接口偶尔不回 user（私密账号的边界情况），此时用调用方给的
        # 用户名兜底，否则整条提取会因为「没有作者」而失败。
        owner = item.get('user') or {}
        if not owner.get('pk'):
            fallback = username(request.get('username'))
            owner = {'pk': media_id or str(item.get('pk') or '0'),
                     'username': fallback, 'full_name': fallback}
        author = mobile_author(owner)
        event = raw_post_data(item, author, request.get('kind'))
        return {'user': author, 'events': [event], 'feedAPI': 'mobile-v1',
                'httpBackend': 'requests'}
    name = username(request.get('query') if operation == 'lookup' else request.get('username'))
    if operation == 'feed_probe':
        user_id = str(request.get('userId') or '')
        if not user_id.isdigit():
            raise Failure('user_unavailable')
        author = {'id': user_id, 'username': name, 'name': name,
                  'avatar': '', 'protected': False}
        events = scan_mobile_items(client.user_posts(user_id), author,
                                   min(5, int(request.get('limit', 1))), 0)
        return {'user': author, 'events': events, 'feedAPI': 'mobile-v1',
                'httpBackend': 'requests'}
    if operation == 'lookup':
        return {'user': mobile_author(client.user_info(name)), 'profileAPI': 'mobile-v1'}
    if operation != 'timeline':
        raise Failure('invalid_request')
    # Prefer the cached id: the usernameinfo call is the request that 429s, and a
    # 429 freezes the account for the whole backoff window.
    author = cached_mobile_author(directory, name)
    if author is None:
        author = mobile_author(client.user_info(name))
        remember_mobile_author(directory, name, author['id'])
    limit = min(max(int(request.get('limit', 100)), 1), 500)
    since = request.get('since') or {}
    events = []
    if request.get('posts', True):
        events.extend(scan_mobile_items(client.user_posts(author['id']), author, limit,
                                        max(int(since.get('post', 0)), 0)))
    if request.get('reels', True):
        events.extend(scan_mobile_items(client.user_clips(author['id']), author, limit,
                                        max(int(since.get('reel', 0)), 0), 'reel'))
    if request.get('stories'):
        events.extend(mobile_story_data(item, author) for item in client.user_story(author['id']))
    dedup = {}
    for event in events:
        key = ('story' if event['kind'] == 'story' else 'post', event['id'])
        if key not in dedup or event['kind'] == 'reel':
            dedup[key] = event
    return {'user': author, 'events': list(dedup.values()),
            'profileAPI': 'mobile-v1', 'feedAPI': 'mobile-v1'}


def story_data(item, author):
    media = {"kind": "image", "url": item.url}
    if item.is_video:
        media = {"kind": "video", "cover": item.url,
                 "variants": [{"url": item.video_url, "bitrate": 1}] if item.video_url else []}
    return {"id": str(item.mediaid), "kind": "story", "author": author, "body": "",
            "url": f'https://www.instagram.com/stories/{author["username"]}/{item.mediaid}/',
            "time": timestamp_ms(item.date_utc), "media": [media], "mediaOrderKnown": True}


def scan_posts(iterator, author, limit, since, forced=None):
    events = []
    old = 0
    for i, post in enumerate(iterator):
        if i >= limit:
            # An incomplete scan must not establish a false success or advance cursors.
            if since and old < 4:
                raise Failure("scan_incomplete")
            break
        stamp = timestamp_ms(post.date_utc)
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
    mobile_path = directory / "mobile-session.json"
    with open(directory / "session.lock", "a") as lock:
        os.chmod(lock.name, 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX)
        limiter = PersistentLimiter(directory)
        backend=request.get("httpBackend", "curl_cffi")
        if backend not in ("curl_cffi", "requests"): raise Failure("invalid_request")
        with limiter.transport(), browser_transport(backend=="curl_cffi"):
            operation = request.get("operation")
            if operation == "session_clear":
                path.unlink(missing_ok=True)
                mobile_path.unlink(missing_ok=True)
                (directory / "pending-2fa.json").unlink(missing_ok=True)
                (directory / "browser-candidate.json").unlink(missing_ok=True)
                (directory / "profile-metadata.json").unlink(missing_ok=True)
                return {"sessionConfigured": False}
            if operation == 'session_mobile_apply':
                proxy = request.get('proxyURL') or ''
                with browser_transport(False):
                    return apply_mobile_session(request, directory, mobile_path, proxy, limiter)
            if mobile_path.exists() and operation in ("lookup", "timeline", "feed_probe", "session_check", "post_detail"):
                if mobile_path.stat().st_mode & 0o077:
                    raise Failure("insecure_session_permissions")
                try:
                    capture = json.loads(mobile_path.read_text())
                except (OSError, ValueError):
                    raise Failure('invalid_session') from None
                proxy = request.get('proxyURL') or ''
                with browser_transport(False):
                    client = MobileClient(
                        capture,
                        proxy_url=proxy,
                        on_state_change=lambda state: write_private(mobile_path, state),
                    )
                    result = execute_mobile(request, client, directory)
                if operation == 'timeline':
                    limiter.success()
                return result
            loader = instaloader.Instaloader(quiet=True, sleep=False, max_connection_attempts=1,
                                           request_timeout=20, rate_controller=limiter.controller, iphone_support=False)
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
                (directory / "profile-metadata.json").unlink(missing_ok=True)
                pending_path.unlink(missing_ok=True)
                return {"username": login, "sessionConfigured": True}
            candidate_path=directory / "browser-candidate.json"
            browser_apply=operation=="session_browser_apply" or (operation in ("lookup","timeline","session_check") and candidate_path.exists())
            candidate=None
            if browser_apply and operation != "session_browser_apply":
                pending=json.loads(candidate_path.read_text())
                if pending.get("rejected"): browser_apply=False
            if browser_apply:
                if not candidate_path.exists():
                    existing=json.loads(path.read_text()) if path.exists() else {}
                    return {"username":existing.get("username",""),"sessionConfigured":bool(existing.get("cookies",{}).get("sessionid"))}
                candidate=json.loads(candidate_path.read_text())
                if candidate.get("rejected"): raise Failure("login_required")
                loader.context.load_session("imported",parse_cookies(json.dumps(candidate.get("cookies",{}))))
            elif operation == "session_import":
                loader.context.load_session("imported", parse_cookies(request.get("cookies", "")))
            elif path.exists():
                if path.stat().st_mode & 0o077:
                    raise Failure("insecure_session_permissions")
                session = json.loads(path.read_text())
                login = username(session["username"])
                loader.context.load_session(login, session["cookies"])
            if proxy:
                loader.context._session.proxies.update({"http": proxy, "https": proxy})
            guest_session = requests.Session()
            if proxy:
                guest_session.proxies.update({"http": proxy, "https": proxy})
            limiter.bind_session(loader.context._session.cookies.get_dict())
            if operation in ("session_import", "session_check", "session_browser_apply") or browser_apply:
                if operation == "session_check" and not login and not browser_apply:
                    raise Failure("login_required")
                verified = loader.test_login()
                if not verified:
                    if browser_apply:
                        candidate["rejected"]=True
                        write_private(candidate_path,candidate)
                        write_private(directory/"browser-status.json",{"configured":True,"pending":False,"error":"浏览器登录态验证失败，已保留原会话"})
                    raise Failure("login_required")
                login = username(verified)
                loader.context.username = login
                write_session(path, loader.context, login)
                (directory / "profile-metadata.json").unlink(missing_ok=True)
                if browser_apply:
                    candidate_path.unlink(missing_ok=True)
                    write_private(directory/"browser-status.json",{"configured":True,"pending":False,"updatedAt":int(time.time()*1000)})
                if operation in ("session_import","session_check","session_browser_apply"):
                    return {"username": login, "sessionConfigured": True}
            if operation not in ("lookup", "timeline", "feed_probe"):
                raise Failure("invalid_request")
            name = username(request.get("query") if operation == "lookup" else request.get("username"))
            if operation == "feed_probe":
                if not login: raise Failure("login_required")
                user_id = str(request.get("userId", ""))
                if not user_id.isdigit(): raise Failure("user_unavailable")
                # Probe the replacement independently of the currently broken profile query.
                author = {"id":user_id,"username":name,"name":name,"avatar":"","protected":False}
                probe_path=directory/"probe-state.json"
                previous=json.loads(probe_path.read_text()) if probe_path.exists() else {}
                attempts=previous.get("attempts",0)
                maximum=min(3,max(1,int(previous.get("maxAttempts",3))))
                compare=bool(previous.get("compareTransport"))
                comparison_path=directory/"transport-comparison.json"
                before=len(limiter.state["requests"])
                try:
                    events=scan_posts(user_posts_v1(loader,user_id,name),author,min(5,int(request.get("limit",1))),0)
                    write_session(path,loader.context,login)
                    if compare and backend=="curl_cffi":
                        limiter.success()
                        comparison={"checkedAt":time.time(),"curl_cffi":{"success":True,"events":len(events)}}
                        try:
                            with browser_transport(False):
                                baseline=scan_posts(user_posts_v1(loader,user_id,name),author,2,0)
                            comparison["requests"]={"success":True,"events":len(baseline)}
                        except RateBlocked as error:
                            comparison["requests"]={"success":False,"error":error.code,"nextRetryAt":error.retry_at}
                        except (FeedError,instaloader.exceptions.InstaloaderException) as error:
                            comparison["requests"]={"success":False,"error":getattr(error,"code","account_unavailable")}
                        write_private(comparison_path,comparison)
                    preference_path=directory/"feed-api.json"
                    preference=json.loads(preference_path.read_text()) if preference_path.exists() else {}
                    preference[name]="v1";write_private(preference_path,preference)
                    write_private(probe_path,{"pending":False,"success":True,"username":name,"userId":user_id,"events":len(events),"attempts":attempts+1,"checkedAt":time.time(),"httpBackend":backend})
                    return {"user":author,"events":events,"feedAPI":"v1","httpBackend":backend}
                except RateBlocked as error:
                    if len(limiter.state["requests"])>before: attempts+=1
                    if compare: write_private(comparison_path,{"checkedAt":time.time(),"curl_cffi":{"success":False,"error":error.code,"nextRetryAt":error.retry_at},"requests":{"skipped":True,"reason":"cooldown"}})
                    write_private(probe_path,{"pending":attempts<maximum,"success":False,"username":name,"userId":user_id,"attempts":attempts,"maxAttempts":maximum,"compareTransport":compare,"httpBackend":backend,"nextRetryAt":error.retry_at,"error":"正在冷却，等待下一次只读验证" if attempts<maximum else "验证受平台限制，已暂停只读验证"})
                    raise
                except FeedError as error:
                    write_private(probe_path,{"pending":False,"success":False,"username":name,"userId":user_id,"attempts":attempts+1,"error":error.code})
                    raise
            guest = None
            # Public profile pages avoid spending the authenticated account's
            # private/Web API budget. A changed guest response may fall back;
            # RateBlocked and SessionBlocked escape from the shared limiter.
            if operation in ("lookup", "timeline") and not request.get("stories"):
                try:
                    guest = guest_profile(guest_session, name)
                except GuestWebError as error:
                    if error.code in ("user_unavailable", "timeout"):
                        raise Failure(error.code)
                    guest = None
            profile = None if guest else resolve_profile(loader.context, name, directory)
            author = guest["user"] if guest else profile_data(profile)
            if operation == "lookup":
                result = {"user": author, "profileAPI": "guest-web" if guest else "instaloader"}
            else:
                if author["protected"] and not login:
                    raise Failure("login_required")
                limit = min(max(int(request.get("limit", 100)), 1), 500)
                since = request.get("since") or {}
                events = []
                if request.get("posts", True):
                    if guest:
                        try:
                            events.extend(scan_posts(public_posts(guest_session, name, guest), author, limit, max(int(since.get("post", 0)), 0)))
                        except GuestWebError as error:
                            if error.code in ("timeout", "user_unavailable", "scan_incomplete"):
                                raise Failure(error.code)
                            guest = None
                            profile = resolve_profile(loader.context, name, directory)
                            author = profile_data(profile)
                    preference_path = directory / "feed-api.json"
                    preference = json.loads(preference_path.read_text()) if preference_path.exists() else {}
                    use_v1 = not guest and login and (preference.get(name) in ("v1","v1-pending"))
                    if use_v1:
                        items = user_posts_v1(loader,profile.userid,name)
                    else:
                        items = None
                    try:
                        if not guest:
                            if items is None: items=profile.get_posts()
                            events.extend(scan_posts(items, author, limit, max(int(since.get("post", 0)), 0)))
                    except RateBlocked:
                        # On the next allowed scan, test v1 instead of retrying the
                        # same failing timeline doc_id forever. Never bypass cooldown.
                        if login and not use_v1:
                            preference[name]="v1-pending";write_private(preference_path,preference)
                        raise
                    except instaloader.exceptions.ConnectionException as error:
                        # Hard API failure can use the alternate; a real cooldown (RateBlocked)
                        # is never bypassed by changing endpoints.
                        if not login or not any(x in str(error) for x in ("400", "401", "403")): raise
                        events.extend(scan_posts(user_posts_v1(loader,profile.userid,name),author,limit,max(int(since.get("post",0)),0)))
                        preference[name]="v1"
                        write_private(preference_path,preference)
                if request.get("reels", True) and not guest:
                    events.extend(scan_posts(user_reels_graphql(profile), author, limit, max(int(since.get("reel", 0)), 0), "reel"))
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
                result = {"user": author, "events": list(dedup.values()),
                          "profileAPI": "guest-web" if guest else "instaloader"}
            if login:
                write_session(path, loader.context, login)
            if operation == "timeline": limiter.success()
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
    except RateBlocked as error:
        output = {"ok": False, "error": {"code": error.code, "nextRetryAt": error.retry_at, "reason": error.reason}}
    except SessionBlocked as error:
        output = {"ok": False, "error": {"code": error.code}}
    except FeedError as error:
        output = {"ok": False, "error": {"code": error.code}}
    except GuestWebError as error:
        output = {"ok": False, "error": {"code": error.code}}
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
        code = "rate_limit" if "429" in message or "too many requests" in message or "wait a few minutes" in message else "checkpoint_required" if "checkpoint" in message else "bad_credentials" if "does not exist" in message else "login_blocked"
        output = {"ok": False, "error": {"code": code}}
    except instaloader.exceptions.ProfileNotExistsException:
        output = {"ok": False, "error": {"code": "user_unavailable"}}
    except instaloader.exceptions.ConnectionException as error:
        # Instaloader may wrap HTTP 429 as a generic ConnectionException.
        message = str(error).lower()
        code = "rate_limit" if "429" in message or "too many requests" in message or "wait a few minutes" in message else "login_required" if any(s in message for s in ("login_required", "login required", "challenge_required", "checkpoint_required")) else "account_unavailable"
        output = {"ok": False, "error": {"code": code}}
    except instaloader.exceptions.PrivateProfileNotFollowedException:
        output = {"ok": False, "error": {"code": "access_denied"}}
    except Exception:
        output = {"ok": False, "error": {"code": "account_unavailable"}}
    print(json.dumps(output, ensure_ascii=False))


if __name__ == "__main__":
    main()
