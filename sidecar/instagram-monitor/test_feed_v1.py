import unittest
from copy import deepcopy
from types import SimpleNamespace
from unittest.mock import patch
import requests
import feed_v1

class FeedTests(unittest.TestCase):
    def mobile_client(self):
        return feed_v1.MobileClient({'headers': {
            'Authorization': 'Bearer test-token',
            'X-IG-App-ID': '567067343352427',
            'User-Agent': 'Instagram test',
            'Accept-Encoding': 'zstd',
        }})

    def test_mobile_session_requires_official_auth_context(self):
        with self.assertRaises(feed_v1.FeedError) as error:
            feed_v1.MobileClient({'headers': {'Authorization': 'Cookie bad'}})
        self.assertEqual(error.exception.code, 'invalid_session')
        self.assertNotEqual(self.mobile_client().session.headers.get('Accept-Encoding'), 'zstd')

    def test_mobile_clips_uses_literal_signature_and_unwraps_media(self):
        response = SimpleNamespace(
            status_code=200,
            headers={},
            json=lambda: {'status': 'ok', 'items': [{'media': {'pk': '7'}}],
                          'paging_info': {}},
        )
        with patch('requests.Session.request', return_value=response) as request:
            self.assertEqual(list(self.mobile_client().user_clips('42')), [{'pk': '7'}])
        call = request.call_args
        self.assertEqual(call.args[:2], ('POST', 'https://i.instagram.com/api/v1/clips/user/'))
        self.assertEqual(
            call.kwargs['data']['signed_body'],
            'SIGNATURE.{"target_user_id":42,"max_id":"","page_size":12,"include_feed_video":"true"}',
        )

    def test_mobile_posts_paginates_without_repeating_cursor(self):
        responses = [
            SimpleNamespace(status_code=200, headers={}, json=lambda: {
                'status': 'ok', 'items': [{'pk': '1'}],
                'more_available': True, 'next_max_id': 'next',
            }),
            SimpleNamespace(status_code=200, headers={}, json=lambda: {
                'status': 'ok', 'items': [{'pk': '2'}], 'more_available': False,
            }),
        ]
        with patch('requests.Session.request', side_effect=responses) as request:
            self.assertEqual([item['pk'] for item in self.mobile_client().user_posts('42')], ['1', '2'])
        self.assertEqual(request.call_args_list[1].kwargs['params']['max_id'], 'next')

    def test_mobile_rotating_state_is_applied_and_persisted(self):
        capture = {'headers': {
            'Authorization': 'Bearer old-token',
            'X-IG-App-ID': '567067343352427',
            'User-Agent': 'Instagram test',
            'X-IG-WWW-Claim': 'old-claim',
        }}
        cookies = requests.cookies.RequestsCookieJar()
        cookies.set('rur', 'new-rur-cookie', domain='.instagram.com', path='/')
        response = SimpleNamespace(
            status_code=200,
            headers={
                'IG-Set-Authorization': 'Bearer new-token',
                'IG-Set-X-MID': 'new-mid',
                'IG-Set-IG-U-RUR': 'new-rur-header',
                'X-IG-Set-WWW-Claim': 'new-claim',
            },
            cookies=cookies,
            json=lambda: {'status': 'ok'},
        )
        saved = []
        client = feed_v1.MobileClient(
            capture,
            on_state_change=lambda state: saved.append(deepcopy(state)),
        )

        with patch('requests.Session.request', return_value=response):
            client.request('GET', 'accounts/current_user/')

        self.assertEqual(client.session.headers['Authorization'], 'Bearer new-token')
        self.assertEqual(client.session.headers['X-IG-WWW-Claim'], 'new-claim')
        self.assertEqual(client.session.headers['IG-U-RUR'], 'new-rur-header')
        self.assertEqual(client.session.cookies.get('rur'), 'new-rur-cookie')
        self.assertEqual(saved[0]['headers']['X-MID'], 'new-mid')
        self.assertEqual(saved[0]['cookies']['rur'], 'new-rur-cookie')

    def test_mobile_state_is_not_persisted_for_failed_response(self):
        saved = []
        response = SimpleNamespace(
            status_code=401,
            headers={'IG-Set-Authorization': 'Bearer replacement'},
            cookies=requests.cookies.RequestsCookieJar(),
        )
        client = feed_v1.MobileClient({'headers': {
            'Authorization': 'Bearer old-token',
            'X-IG-App-ID': '567067343352427',
            'User-Agent': 'Instagram test',
        }}, on_state_change=saved.append)
        with patch('requests.Session.request', return_value=response):
            with self.assertRaises(feed_v1.FeedError) as error:
                client.request('GET', 'accounts/current_user/')
        self.assertEqual(error.exception.code, 'login_required')
        self.assertEqual(saved, [])

    def test_reels_reuse_media_and_reject_unrelated_authors(self):
        profile = SimpleNamespace(_context=object(), userid=42, username='artist')
        media = {'pk': '1', 'code': 'abc', 'media_type': 2, 'taken_at': 100,
                 'user': {'pk': '42'}}
        with patch.object(feed_v1.instaloader, 'NodeIterator') as iterator, patch.object(feed_v1.instaloader.Post, 'from_iphone_struct', return_value='post') as convert, patch.object(feed_v1.instaloader.Post, 'from_shortcode') as detail:
            feed_v1.user_reels_graphql(profile)
            wrap = iterator.call_args.kwargs['node_wrapper']
            self.assertEqual(wrap({'media': media}), 'post')
            self.assertEqual(convert.call_args.args[1]['has_liked'], False)
            detail.assert_not_called()
            with self.assertRaises(feed_v1.FeedError) as error:
                wrap({'media': {**media, 'user': {'pk': 'other'}}})
            self.assertEqual(error.exception.code, 'user_unavailable')
            with self.assertRaises(feed_v1.FeedError):
                wrap({'media': {'code': 'partial'}})

    def test_pagination_and_cursor_preserved(self):
        responses=[SimpleNamespace(status_code=200,json=lambda:{'items':[{'pk':1,'user':{'pk':42}}],'more_available':True,'next_max_id':'next'}),SimpleNamespace(status_code=200,json=lambda:{'items':[{'pk':2,'user':{'pk':42}}],'more_available':False})]
        with patch.object(feed_v1.instaloader.Post,'from_iphone_struct',side_effect=lambda ctx,item:item['pk']), patch('requests.Session.get',side_effect=responses) as get:
            import requests
            loader=SimpleNamespace(context=SimpleNamespace(_session=requests.Session(),request_timeout=20))
            self.assertEqual(list(feed_v1.user_posts_v1(loader,'42','artist')),[1,2])
            self.assertEqual(get.call_args_list[1].kwargs['params']['max_id'],'next')

    def test_missing_items_is_not_empty_success(self):
        import requests
        loader=SimpleNamespace(context=SimpleNamespace(_session=requests.Session(),request_timeout=20))
        with patch('requests.Session.get',return_value=SimpleNamespace(status_code=200,json=lambda:{'status':'fail'})):
            with self.assertRaises(feed_v1.FeedError): list(feed_v1.user_posts_v1(loader,'42','artist'))

    def test_transport_errors_are_sanitized(self):
        import requests
        loader=SimpleNamespace(context=SimpleNamespace(_session=requests.Session(),request_timeout=20))
        with patch('requests.Session.get',side_effect=requests.exceptions.Timeout('private URL')):
            with self.assertRaises(feed_v1.FeedError) as error:list(feed_v1.user_posts_v1(loader,'42','artist'))
        self.assertEqual(error.exception.code,'timeout')


class MediaInfoTest(unittest.TestCase):
    """单条媒体端点：链接提取靠它，不能退化成拉整条时间线。"""

    def client(self, items):
        client = feed_v1.MobileClient({'headers': {
            'Authorization': 'Bearer x', 'X-IG-App-ID': '123',
            'User-Agent': 'Instagram Android'}})
        client.request = lambda method, path, **kw: {'items': items}
        return client

    def test_returns_first_media(self):
        client = self.client([{'pk': 1}, {'pk': 2}])
        self.assertEqual(client.media_info('123')['pk'], 1)

    def test_rejects_non_numeric_id_without_request(self):
        # 非数字 id 必须在发请求前就被挡掉，否则等于把任意字符串拼进 URL。
        client = self.client([{'pk': 1}])
        with self.assertRaises(feed_v1.FeedError) as error:
            client.media_info('../admin')
        self.assertEqual(error.exception.code, 'user_unavailable')

    def test_empty_items_is_not_found(self):
        # 空列表必须报错，不能当成「取到了空内容」往上层返回。
        client = self.client([])
        with self.assertRaises(feed_v1.FeedError) as error:
            client.media_info('123')
        self.assertEqual(error.exception.code, 'user_unavailable')

    def test_non_dict_first_item_is_rejected(self):
        client = self.client(['not-a-dict'])
        with self.assertRaises(feed_v1.FeedError):
            client.media_info('123')

if __name__ == '__main__': unittest.main()
