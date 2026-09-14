import datetime
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import collector as c


class CollectorTests(unittest.TestCase):
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
        target = SimpleNamespace(username='Hearts2Hearts')
        with patch.object(c.instaloader, 'TopSearchResults') as search, patch.object(c.instaloader.Profile, 'from_username') as direct:
            search.return_value.get_profiles.return_value = iter([SimpleNamespace(username='hearts2hearts.fan'), target])
            self.assertIs(c.resolve_profile(context, 'hearts2hearts'), target)
            direct.assert_not_called()
        with patch.object(c.instaloader, 'TopSearchResults') as search:
            search.return_value.get_profiles.return_value = iter([SimpleNamespace(username='other')])
            with self.assertRaises(c.Failure):
                c.resolve_profile(context, 'hearts2hearts')

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
                with patch.object(c, 'resolve_profile') as profile:
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

if __name__ == '__main__':
    unittest.main()
