import unittest
from types import SimpleNamespace
from unittest.mock import patch
import feed_v1

class FeedTests(unittest.TestCase):
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

if __name__ == '__main__': unittest.main()
