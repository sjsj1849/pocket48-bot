"""Authenticated web feed adapter; protocol reference: Instaloader issue #2689."""
import instaloader
import requests


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
