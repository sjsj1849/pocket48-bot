import datetime
import base64
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import collector as c


class CollectorTests(unittest.TestCase):
    @staticmethod
    def mobile_token(user_id, session_id):
        payload = json.dumps({'ds_user_id': user_id, 'sessionid': session_id}, separators=(',', ':')).encode()
        return 'Bearer IGT:2:' + base64.b64encode(payload).decode()

    def mobile_capture(self, token):
        return {
            'headers': {
                'Authorization': token,
                'User-Agent': 'Instagram test Android',
                'X-IG-App-ID': '567067343352427',
                'X-IG-Device-ID': 'device-old',
            },
            'cookies': {
                'rur': 'old-rur',
            },
        }

    def test_mobile_candidate_validates_then_atomically_replaces_and_backs_up(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            old = self.mobile_capture(self.mobile_token('12345678901', 'old-session'))
            new_token = self.mobile_token('12345678901', 'new-session')
            c.write_private(directory / 'mobile-session.json', old)
            with patch.object(c, 'MobileClient') as client_class, patch.object(
                    c, 'execute_mobile', return_value={'username': 'reader', 'sessionConfigured': True}):
                client_class.return_value.capture = None
                result = c.apply_mobile_session(
                    {'authorization': new_token, 'headers': {'X-IG-Device-ID': 'device-new'}},
                    directory,
                    directory / 'mobile-session.json',
                    '',
                )
            stored = json.loads((directory / 'mobile-session.json').read_text())
            self.assertEqual(stored['headers']['Authorization'], new_token)
            self.assertEqual(stored['headers']['X-IG-Device-ID'], 'device-new')
            self.assertEqual(stored['cookies'], old['cookies'])
            backups = list((directory / 'session-backups').glob('mobile-session.*.json'))
            self.assertEqual(len(backups), 1)
            self.assertEqual(json.loads(backups[0].read_text()), old)
            self.assertTrue(result['sessionUpdated'])
            self.assertEqual((directory / 'mobile-session.json').stat().st_mode & 0o777, 0o600)

    def test_mobile_candidate_failure_or_account_mismatch_preserves_existing_session(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            old = self.mobile_capture(self.mobile_token('12345678901', 'old-session'))
            path = directory / 'mobile-session.json'
            c.write_private(path, old)
            same_account = self.mobile_token('12345678901', 'new-session')
            with patch.object(c, 'MobileClient'), patch.object(
                    c, 'execute_mobile', side_effect=c.FeedError('login_required')):
                with self.assertRaises(c.FeedError):
                    c.apply_mobile_session({'authorization': same_account, 'headers': {}}, directory, path, '')
            self.assertEqual(json.loads(path.read_text()), old)
            self.assertFalse((directory / 'session-backups').exists())

            other_account = self.mobile_token('99999999999', 'other-session')
            with self.assertRaises(c.Failure) as error:
                c.apply_mobile_session({'authorization': other_account, 'headers': {}}, directory, path, '')
            self.assertEqual(error.exception.code, 'session_account_mismatch')
            self.assertEqual(json.loads(path.read_text()), old)

    def test_cached_profile_skips_search_and_checks_identity(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / 'profile-cache.json').write_text(json.dumps({'artist': '42'}))
            context = SimpleNamespace(is_logged_in=True)
            with patch.object(c.instaloader, 'Profile') as profile_class, patch.object(c.instaloader, 'TopSearchResults') as search:
                profile = profile_class.return_value
                profile.userid = 42
                profile.username = 'artist'
                self.assertIs(c.resolve_profile(context, 'artist', Path(directory)), profile)
                profile._obtain_metadata.assert_called_once()
                search.assert_not_called()
                profile.username = 'other'
                with self.assertRaises(c.Failure):
                    c.resolve_profile(context, 'artist', Path(directory))

    def test_naive_instaloader_dates_are_utc(self):
        self.assertEqual(c.timestamp_ms(datetime.datetime(2026, 9, 15, 12)), c.timestamp_ms(datetime.datetime(2026, 9, 15, 12, tzinfo=datetime.timezone.utc)))

    def test_authenticated_lookup_uses_exact_search_result(self):
        context = SimpleNamespace(is_logged_in=True)
        target = SimpleNamespace(username='Hearts2Hearts', _obtain_metadata=lambda: None)
        with patch.object(c.instaloader, 'TopSearchResults') as search, patch.object(c.instaloader.Profile, 'from_username') as direct:
            search.return_value.get_profiles.return_value = iter([SimpleNamespace(username='hearts2hearts.fan'), target])
            self.assertIs(c.resolve_profile(context, 'hearts2hearts'), target)
            direct.assert_not_called()
        with patch.object(c.instaloader, 'TopSearchResults') as search:
            search.return_value.get_profiles.return_value = iter([SimpleNamespace(username='other')])
            with self.assertRaises(c.Failure):
                c.resolve_profile(context, 'hearts2hearts')

    def test_guest_rate_limit_never_falls_back_to_authenticated_lookup(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(c, 'guest_profile', side_effect=c.RateBlocked(c.time.time() + 900, 'rate_limit')), \
                    patch.object(c, 'resolve_profile') as authenticated:
                with self.assertRaises(c.RateBlocked):
                    c.execute({'operation': 'lookup', 'query': 'artist'}, Path(directory))
                authenticated.assert_not_called()

    def test_profile_metadata_cache_avoids_duplicate_request_and_expires(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            profile = SimpleNamespace(userid=42, username='artist', _node={'id': '42', 'username': 'artist'}, _obtain_metadata=lambda: None)
            with patch.object(c.instaloader, 'TopSearchResults') as search:
                search.return_value.get_profiles.return_value = iter([profile])
                c.resolve_profile(SimpleNamespace(is_logged_in=True), 'artist', path)
            with patch.object(c.instaloader.Profile, '_obtain_metadata') as obtain, patch.object(c.instaloader, 'TopSearchResults') as search:
                cached = c.resolve_profile(SimpleNamespace(is_logged_in=True), 'artist', path)
                self.assertEqual(cached.userid, 42)
                obtain.assert_not_called()
                search.assert_not_called()
            with patch.object(c.time, 'time', return_value=c.time.time() + 601), patch.object(c.instaloader, 'Profile') as constructor:
                constructor.return_value = profile
                c.resolve_profile(SimpleNamespace(is_logged_in=True), 'artist', path)
                constructor.assert_called_once()

    def test_list_media_is_complete_without_per_post_detail_request(self):
        item = {'pk': '123', 'code': 'abc', 'taken_at': 100, 'media_type': 8,
                'caption': {'text': 'album'}, 'carousel_media': [
                    {'media_type': 1, 'image_versions2': {'candidates': [{'url': 'small', 'width': 100, 'height': 100}, {'url': 'large', 'width': 1000, 'height': 1000}]}},
                    {'media_type': 2, 'video_versions': [{'url': 'low', 'width': 100, 'height': 100}, {'url': 'high', 'width': 1080, 'height': 1920}]},
                ]}
        event = c.post_data(SimpleNamespace(_node={'iphone_struct': item}), {'id': '42'})
        self.assertEqual(event['body'], 'album')
        self.assertEqual(event['media'][0]['url'], 'large')
        self.assertEqual([variant['url'] for variant in event['media'][1]['variants']], ['low', 'high'])
        item['carousel_media'][1]['video_versions'] = []
        with self.assertRaises(c.Failure) as error:
            c.raw_post_data(item, {'id': '42'})
        self.assertEqual(error.exception.code, 'scan_incomplete')

    def test_cookies_domain_and_secret_validation(self):
        self.assertEqual(c.parse_cookies('sessionid=abc%3A123; csrftoken=def; junk=x'), {'sessionid': 'abc%3A123', 'csrftoken': 'def'})
        with self.assertRaises(c.Failure):
            c.parse_cookies('[{"name":"sessionid","value":"abc","domain":"evil.test"},{"name":"csrftoken","value":"def","domain":"evil.test"}]')

    def test_mixed_album_preserves_order(self):
        nodes = [SimpleNamespace(is_video=False, display_url='https://cdn/a.jpg'), SimpleNamespace(is_video=True, display_url='https://cdn/b.jpg', video_url='https://cdn/b.mp4')]
        post = SimpleNamespace(_node={}, typename='GraphSidecar', get_sidecar_nodes=lambda: nodes, mediaid=123, caption='caption', shortcode='abc', date_utc=datetime.datetime(2026, 9, 15, tzinfo=datetime.timezone.utc))
        event = c.post_data(post, {'id': '42'})
        self.assertEqual([m['kind'] for m in event['media']], ['image', 'video'])
        self.assertEqual(event['media'][1]['variants'][0]['url'], 'https://cdn/b.mp4')

    def test_pinned_old_posts_do_not_hide_recent(self):
        author = {'id': '42'}
        def post(i, stamp):
            return SimpleNamespace(_node={}, typename='GraphImage', mediaid=i, caption='', shortcode=str(i), date_utc=datetime.datetime.fromtimestamp(stamp, datetime.timezone.utc), url='https://cdn/a.jpg', is_video=False)
        posts = [post(1, 1), post(2, 2), post(3, 3), post(4, 100), post(5, 101)]
        self.assertEqual([e['id'] for e in c.scan_posts(iter(posts), author, 100, 90000)], ['4', '5'])
        with self.assertRaises(c.Failure) as err:
            c.scan_posts(iter([post(i, 100+i) for i in range(10)]), author, 3, 90000)
        self.assertEqual(err.exception.code, 'scan_incomplete')

    def test_failed_import_preserves_existing_session(self):
        with tempfile.TemporaryDirectory() as directory:
            p = Path(directory) / 'session.json'
            p.write_text('old-private-session')
            with patch.object(c.instaloader, 'Instaloader') as constructor:
                constructor.return_value.test_login.return_value = None
                with self.assertRaises(c.Failure):
                    c.execute({'operation': 'session_import', 'cookies': 'sessionid=a; csrftoken=b'}, Path(directory))
            self.assertEqual(p.read_text(), 'old-private-session')

    def test_browser_candidate_validation_preserves_good_session(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            old = {'username': 'me', 'cookies': {'sessionid': 'good', 'csrftoken': 'csrf'}}
            c.write_private(directory / 'session.json', old)
            c.write_private(directory / 'browser-candidate.json', {'cookies': {'sessionid': 'new', 'csrftoken': 'new-csrf'}})
            with patch.object(c.instaloader, 'Instaloader') as constructor:
                constructor.return_value.test_login.return_value = None
                constructor.return_value.context.save_session.return_value = old['cookies']
                with self.assertRaises(c.Failure):
                    c.execute({'operation': 'session_browser_apply'}, directory)
                self.assertEqual(json.loads((directory / 'session.json').read_text()), old)
                self.assertTrue(json.loads((directory / 'browser-candidate.json').read_text())['rejected'])
                with patch.object(c, 'guest_profile', side_effect=c.GuestWebError('guest_unavailable')), \
                        patch.object(c, 'resolve_profile') as profile:
                    profile.return_value = SimpleNamespace(userid=42, username='artist', full_name='Artist', _node={'profile_pic_url':'https://cdn/avatar'}, is_private=False)
                    c.execute({'operation': 'lookup', 'query': 'artist'}, directory)
                    profile.assert_called_once()

    def test_browser_candidate_success_and_clear_keep_rate_state(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            c.write_private(directory / 'browser-candidate.json', {'cookies': {'sessionid': 'new', 'csrftoken': 'new-csrf'}})
            rate = {'requests': [c.time.time()], 'builtin': {}, 'failureStreak': 2}
            c.write_private(directory / 'request-state.json', rate)
            with patch.object(c.instaloader, 'Instaloader') as constructor:
                loader = constructor.return_value
                loader.test_login.return_value = 'me'
                loader.context.save_session.return_value = {'sessionid': 'new', 'csrftoken': 'new-csrf'}
                c.execute({'operation': 'session_browser_apply'}, directory)
            self.assertFalse((directory / 'browser-candidate.json').exists())
            self.assertEqual(json.loads((directory / 'session.json').read_text())['cookies']['sessionid'], 'new')
            c.execute({'operation': 'session_clear'}, directory)
            self.assertEqual(json.loads((directory / 'request-state.json').read_text()), rate)

    def test_password_login_saves_only_session(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(c.instaloader, 'Instaloader') as constructor:
                loader = constructor.return_value
                loader.context.username = 'me'
                loader.context.save_session.return_value = {'sessionid': 'session', 'csrftoken': 'csrf'}
                result = c.execute({'operation': 'session_login', 'username': 'me', 'password': 'very-secret'}, Path(directory))
                self.assertTrue(result['sessionConfigured'])
                stored = (Path(directory) / 'session.json').read_text()
                self.assertNotIn('very-secret', stored)
                self.assertEqual((Path(directory) / 'session.json').stat().st_mode & 0o777, 0o600)

    def test_2fa_preserves_old_session_until_verification(self):
        import json
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'session.json'
            path.write_text('old-session')
            with patch.object(c.instaloader, 'Instaloader') as constructor:
                loader = constructor.return_value
                loader.login.side_effect = c.instaloader.exceptions.TwoFactorAuthRequiredException('required')
                session = SimpleNamespace(cookies=SimpleNamespace(get_dict=lambda: {'csrftoken': 'csrf'}))
                loader.context.two_factor_auth_pending = (session, 'me', 'identifier')
                with self.assertRaises(c.Failure) as err:
                    c.execute({'operation': 'session_login', 'username': 'me', 'password': 'very-secret'}, Path(directory))
                self.assertEqual(err.exception.code, 'two_factor_required')
                self.assertEqual(path.read_text(), 'old-session')
                pending = (Path(directory) / 'pending-2fa.json').read_text()
                self.assertNotIn('very-secret', pending)
                self.assertTrue(json.loads(pending)['expires'] > c.time.time())

    def test_timeline_reuses_cached_user_id_without_usernameinfo(self):
        # The redundant usernameinfo call was what earned the 429s that then
        # froze the account, so a cached id must be used without any request.
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            c.write_private(directory / 'profile-cache.json', {'hearts2hearts': '71826772894'})
            client = SimpleNamespace()
            client.user_info = lambda name: self.fail('usernameinfo must not be called when the id is cached')
            client.user_posts = lambda uid: iter([])
            client.user_clips = lambda uid: iter([])
            result = c.execute_mobile(
                {'operation': 'timeline', 'username': 'hearts2hearts', 'limit': 5,
                 'posts': True, 'reels': True}, client, directory)
            self.assertEqual(result['user']['id'], '71826772894')
            self.assertEqual(result['user']['username'], 'hearts2hearts')

    def test_timeline_falls_back_to_usernameinfo_when_id_unknown_then_caches(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            client = SimpleNamespace()
            client.user_info = lambda name: {'pk': '71826772894', 'username': 'hearts2hearts',
                                             'full_name': 'Hearts'}
            client.user_posts = lambda uid: iter([])
            client.user_clips = lambda uid: iter([])
            c.execute_mobile({'operation': 'timeline', 'username': 'hearts2hearts',
                              'limit': 5, 'posts': True, 'reels': True}, client, directory)
            cached = json.loads((directory / 'profile-cache.json').read_text())
            self.assertEqual(cached['hearts2hearts'], '71826772894')

    def test_feed_item_fills_display_fields_of_cached_author(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            c.write_private(directory / 'profile-cache.json', {'hearts2hearts': '71826772894'})
            author = c.cached_mobile_author(directory, 'hearts2hearts')
            self.assertEqual(author['avatar'], '')
            item = {'pk': '4000000001', 'code': 'ABC123', 'taken_at': 1700000000, 'media_type': 1,
                    'user': {'pk': '71826772894', 'username': 'hearts2hearts',
                             'full_name': 'Hearts', 'profile_pic_url': 'https://cdn/avatar.jpg'},
                    'image_versions2': {'candidates': [{'url': 'https://cdn/1.jpg', 'width': 1080}]}}
            events = c.scan_mobile_items(iter([item]), author, 5, 0)
            self.assertEqual(author['name'], 'Hearts')
            self.assertEqual(author['avatar'], 'https://cdn/avatar.jpg')
            self.assertEqual(len(events), 1)

    def test_candidate_apply_is_exempt_from_cooldown_and_thaws(self):
        # Regression: cooldown used to block the very call that recovers a dead
        # session, so the backoff could never be cleared from the phone.
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            path = directory / 'mobile-session.json'
            c.write_private(path, self.mobile_capture(self.mobile_token('12345678901', 'old')))
            limiter = SimpleNamespace(exempt=False, thawed=False)
            limiter.thaw = lambda: setattr(limiter, 'thawed', True)

            def blocked_without_exempt(*args, **kwargs):
                if not limiter.exempt:
                    raise AssertionError('recovery ran while still rate limited')
                return {'username': 'reader', 'sessionConfigured': True}

            with patch.object(c, 'MobileClient'), patch.object(c, 'execute_mobile', blocked_without_exempt):
                result = c.apply_mobile_session(
                    {'authorization': self.mobile_token('12345678901', 'new'), 'headers': {}},
                    directory, path, '', limiter)
            self.assertTrue(result['sessionUpdated'])
            self.assertTrue(limiter.thawed)
            self.assertFalse(limiter.exempt)

    def test_candidate_apply_restores_exempt_when_validation_raises(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            path = directory / 'mobile-session.json'
            old = self.mobile_capture(self.mobile_token('12345678901', 'old'))
            c.write_private(path, old)
            limiter = SimpleNamespace(exempt=False, thawed=False)
            limiter.thaw = lambda: setattr(limiter, 'thawed', True)
            with patch.object(c, 'MobileClient'), patch.object(c, 'execute_mobile', side_effect=c.FeedError('x')):
                with self.assertRaises(c.FeedError):
                    c.apply_mobile_session(
                        {'authorization': self.mobile_token('12345678901', 'new'), 'headers': {}},
                        directory, path, '', limiter)
            self.assertEqual(json.loads(path.read_text()), old)
            self.assertFalse(limiter.exempt)
            self.assertFalse(limiter.thawed)

if __name__ == '__main__':
    unittest.main()
