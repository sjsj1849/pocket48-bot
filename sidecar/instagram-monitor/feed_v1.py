"""Instagram feed adapters for legacy web sessions and captured mobile sessions."""
import json
import re

import instaloader
import requests


MOBILE_API = 'https://i.instagram.com/api/v1'

# 短码只允许字母数字下划线连字符：它会被拼进请求路径，必须先挡掉斜杠与空格。
SHORTCODE_RE = re.compile(r'^[A-Za-z0-9_-]{1,64}$')
MOBILE_DROP_HEADERS = {
    'accept-encoding', 'content-length', 'host', 'priority',
    'x-fb-client-ip', 'x-fb-server-cluster', 'x-tigon-is-retry',
    'x-fb-http-engine', 'x-fb-request-analytics-tags',
    'x-meta-tasos-congestion-config', 'x-meta-tasos-dcc-constraint',
    'x-fb-tasos-td-v2-config',
}
MOBILE_ROTATING_HEADERS = {
    'ig-set-authorization': 'Authorization',
    'ig-set-x-mid': 'X-MID',
    'ig-set-ig-u-rur': 'IG-U-RUR',
    'ig-set-ig-u-ds-user-id': 'IG-U-DS-USER-ID',
    'ig-set-ig-u-shbid': 'IG-U-SHBID',
    'ig-set-ig-u-shbts': 'IG-U-SHBTS',
    'ig-set-ig-u-ig-direct-region-hint': 'IG-U-IG-DIRECT-REGION-HINT',
    'x-ig-set-www-claim': 'X-IG-WWW-Claim',
}
MOBILE_ROTATING_COOKIES = {'rur'}


class MobileClient:
    """Replay the official app's Bearer/device context without exposing it to logs."""

    def __init__(self, capture, proxy_url='', timeout=20, on_state_change=None):
        source = capture.get('headers') if isinstance(capture, dict) else None
        if not isinstance(source, dict):
            raise FeedError('invalid_session')
        headers = {}
        for name, value in source.items():
            if not isinstance(name, str) or not isinstance(value, str):
                raise FeedError('invalid_session')
            if len(name) > 256 or len(value) > 16384 or '\n' in name + value or '\r' in name + value:
                raise FeedError('invalid_session')
            if name.lower() not in MOBILE_DROP_HEADERS:
                headers[name] = value
        lowered = {name.lower(): value for name, value in headers.items()}
        if not lowered.get('authorization', '').startswith('Bearer '):
            raise FeedError('invalid_session')
        if not lowered.get('x-ig-app-id') or not lowered.get('user-agent'):
            raise FeedError('invalid_session')
        self.session = requests.Session()
        self.session.headers.update(headers)
        cookies = capture.get('cookies', {})
        if not isinstance(cookies, dict):
            raise FeedError('invalid_session')
        for name, value in cookies.items():
            if name not in MOBILE_ROTATING_COOKIES or not isinstance(value, str):
                raise FeedError('invalid_session')
            if len(value) > 4096 or '\n' in value or '\r' in value:
                raise FeedError('invalid_session')
            self.session.cookies.set(name, value, domain='.instagram.com', path='/')
        if proxy_url:
            self.session.proxies.update({'http': proxy_url, 'https': proxy_url})
        self.capture = capture
        self.on_state_change = on_state_change
        self.timeout = timeout

    def _set_capture_header(self, name, value):
        source = self.capture['headers']
        existing = next((key for key in source if key.lower() == name.lower()), None)
        key = existing or name
        changed = source.get(key) != value
        source[key] = value
        self.session.headers[name] = value
        return changed

    def _apply_response_state(self, response):
        changed = False
        response_headers = requests.structures.CaseInsensitiveDict(response.headers)
        for response_name, request_name in MOBILE_ROTATING_HEADERS.items():
            value = response_headers.get(response_name)
            if not value:
                continue
            if not isinstance(value, str) or len(value) > 16384 or '\n' in value or '\r' in value:
                continue
            if request_name == 'Authorization' and not value.startswith('Bearer '):
                continue
            changed = self._set_capture_header(request_name, value) or changed

        for cookie in getattr(response, 'cookies', ()):
            if cookie.name.lower() not in MOBILE_ROTATING_COOKIES:
                continue
            value = cookie.value
            if not isinstance(value, str) or len(value) > 4096 or '\n' in value or '\r' in value:
                continue
            cookies = self.capture.setdefault('cookies', {})
            changed = cookies.get(cookie.name.lower()) != value or changed
            cookies[cookie.name.lower()] = value
            self.session.cookies.set(cookie.name.lower(), value,
                                     domain='.instagram.com', path='/')

        if changed and self.on_state_change:
            try:
                self.on_state_change(self.capture)
            except OSError:
                raise FeedError('account_unavailable') from None

    def request(self, method, path, **kwargs):
        try:
            response = self.session.request(
                method,
                f'{MOBILE_API}/{path.lstrip("/")}',
                timeout=self.timeout,
                **kwargs,
            )
        except requests.exceptions.Timeout:
            raise FeedError('timeout') from None
        except requests.exceptions.RequestException:
            raise FeedError('account_unavailable') from None
        if response.status_code == 429:
            raise FeedError('rate_limit')
        if response.status_code in (401, 403):
            raise FeedError('login_required')
        if response.status_code != 200:
            raise FeedError('account_unavailable')
        try:
            payload = response.json()
        except ValueError:
            raise FeedError('account_unavailable') from None
        if not isinstance(payload, dict) or payload.get('status', 'ok') != 'ok':
            raise FeedError('account_unavailable')
        self._apply_response_state(response)
        return payload

    def current_user(self):
        return self.request('GET', 'accounts/current_user/', params={'edit': 'true'})['user']

    def user_info(self, username):
        payload = self.request('GET', f'users/{username}/usernameinfo/')
        user = payload.get('user')
        if not isinstance(user, dict):
            raise FeedError('user_unavailable')
        return user

    def user_posts(self, user_id):
        cursor = ''
        seen = set()
        for _ in range(50):
            params = {'count': 12}
            if cursor:
                params['max_id'] = cursor
            payload = self.request('GET', f'feed/user/{user_id}/', params=params)
            items = payload.get('items')
            if not isinstance(items, list):
                raise FeedError('account_unavailable')
            yield from items
            if not payload.get('more_available'):
                return
            cursor = str(payload.get('next_max_id') or '')
            if not cursor or cursor in seen:
                raise FeedError('scan_incomplete')
            seen.add(cursor)
        raise FeedError('scan_incomplete')

    def user_clips(self, user_id):
        cursor = ''
        seen = set()
        for _ in range(50):
            body = json.dumps({
                'target_user_id': int(user_id),
                'max_id': cursor,
                'page_size': 12,
                'include_feed_video': 'true',
            }, separators=(',', ':'))
            payload = self.request(
                'POST',
                'clips/user/',
                data={'signed_body': f'SIGNATURE.{body}'},
            )
            items = payload.get('items')
            if not isinstance(items, list):
                raise FeedError('account_unavailable')
            for item in items:
                media = item.get('media') if isinstance(item, dict) else None
                if not isinstance(media, dict):
                    raise FeedError('scan_incomplete')
                yield media
            cursor = str((payload.get('paging_info') or {}).get('max_id') or '')
            if not cursor:
                return
            if cursor in seen:
                raise FeedError('scan_incomplete')
            seen.add(cursor)
        raise FeedError('scan_incomplete')

    def user_story(self, user_id):
        reel = self.request('GET', f'feed/user/{user_id}/story/').get('reel') or {}
        items = reel.get('items') or []
        if not isinstance(items, list):
            raise FeedError('account_unavailable')
        return items

    def media_info(self, media_id):
        """取单条媒体的完整信息（含 video_versions）。

        链接提取走这个端点而不是拉整个时间线：一次时间线请求要翻页，
        而用户只想看某一条。取不到就报 user_unavailable，
        上层会翻译成「内容不存在、已删除，或需要登录才能查看」。
        """
        if not media_id.isdigit():
            raise FeedError('user_unavailable')
        payload = self.request('GET', f'media/{media_id}/info/')
        items = payload.get('items')
        if not isinstance(items, list) or not items:
            raise FeedError('user_unavailable')
        item = items[0]
        if not isinstance(item, dict):
            raise FeedError('user_unavailable')
        return item

    def media_by_code(self, code, user_id):
        """按短码取单条媒体（先列表定位，再取详情）。

        ★ 为什么不能直接请求 media/shortcode/<code>/info/：
        实测（2026-10-09）该端点已被 Instagram 下线，返回 **404 + HTML 页面**
        （不是 JSON 错误体），几种变体（去掉 /info/、web_info/、web/ 前缀）
        全部 404。只有 media/<数字id>/info/ 仍可用。

        而分享链接里只有短码、没有数字 id，所以必须两步：
          1. 在该作者的帖子与 Reels 列表里按 code 精确匹配，拿到 pk；
          2. 用 pk 取详情 —— 详情里才有 video_versions
             （列表项对轮播帖只给 carousel_media，顶层没有视频档位）。

        代价是第 1 步可能要翻页，但命中即停，比拉完整时间线便宜。
        user_id 由调用方从链接里的用户名解析得到。
        """
        if not SHORTCODE_RE.match(code or ''):
            raise FeedError('user_unavailable')
        if not str(user_id or '').isdigit():
            raise FeedError('user_unavailable')
        for iterator in (self.user_posts, self.user_clips):
            for item in iterator(user_id):
                if str(item.get('code') or '') == code:
                    pk = str(item.get('pk') or '')
                    if pk.isdigit():
                        return self.media_info(pk)
                    raise FeedError('user_unavailable')
        raise FeedError('user_unavailable')


def user_reels_graphql(profile):
    """Reuse media in the Reels page instead of resolving every shortcode."""
    def wrap(node):
        media = node.get('media') or {}
        if not all(key in media for key in ('pk', 'code', 'media_type', 'taken_at')):
            raise FeedError('scan_incomplete')
        owner = str((media.get('user') or {}).get('pk') or '')
        collaborators = [str(user.get('pk')) for user in media.get('coauthor_producers') or []]
        if owner != str(profile.userid) and str(profile.userid) not in collaborators:
            raise FeedError('user_unavailable')
        media = {**media, 'has_liked': media.get('has_liked', False),
                 'like_count': media.get('like_count', 0)}
        return instaloader.Post.from_iphone_struct(profile._context, media)

    return instaloader.NodeIterator(
        context=profile._context, query_hash=None,
        edge_extractor=lambda data: data['data']['xdt_api__v1__clips__user__connection_v2'],
        node_wrapper=wrap,
        query_variables={'data': {'page_size': 12, 'include_feed_video': True,
                                 'target_user_id': str(profile.userid)}},
        query_referer=f'https://www.instagram.com/{profile.username}/',
        doc_id='7845543455542541',
    )


class FeedError(Exception):
    def __init__(self, code):
        self.code = code


def user_posts_v1(loader, user_id, username):
    session = loader.context._session
    url = f'https://www.instagram.com/api/v1/feed/user/{user_id}/'
    headers = {'X-IG-App-ID': '936619743392459', 'X-ASBD-ID': '198387',
               'X-Requested-With': 'XMLHttpRequest',
               'Referer': f'https://www.instagram.com/{username}/',
               'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15'}
    cursor = None
    seen_cursors = set()
    for _ in range(50):
        params = {'count': 12}
        if cursor:
            params['max_id'] = cursor
        try:
            response = session.get(url, params=params, headers=headers, timeout=loader.context.request_timeout)
        except requests.exceptions.Timeout:
            raise FeedError("timeout") from None
        except requests.exceptions.RequestException:
            raise FeedError("account_unavailable") from None
        if response.status_code in (401, 403):
            raise FeedError('login_required')
        if response.status_code != 200:
            raise FeedError('account_unavailable')
        try:
            data = response.json()
        except ValueError:
            raise FeedError('account_unavailable')
        # A redirect/login HTML or changed shape must never appear as an empty feed.
        if 'items' not in data or not isinstance(data['items'], list) or data.get('status', 'ok') != 'ok':
            raise FeedError('account_unavailable')
        for item in data['items']:
            owner = item.get('user', {}).get('pk')
            if owner and str(owner) != str(user_id) and not any(str(x.get("pk"))==str(user_id) for x in item.get("coauthor_producers",[])):
                raise FeedError('user_unavailable')
            yield instaloader.Post.from_iphone_struct(loader.context, item)
        if not data.get('more_available'):
            return
        cursor = data.get('next_max_id')
        if not cursor or cursor in seen_cursors:
            raise FeedError('scan_incomplete')
        seen_cursors.add(cursor)
    raise FeedError('scan_incomplete')
