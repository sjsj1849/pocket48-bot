import unittest
from types import SimpleNamespace
from unittest.mock import patch
import feed_v1

class FeedTests(unittest.TestCase):
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

if __name__ == '__main__': unittest.main()
