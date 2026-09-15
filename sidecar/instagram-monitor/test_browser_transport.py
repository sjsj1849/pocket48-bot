import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import requests
from curl_cffi.requests.headers import Headers
from browser_transport import browser_transport, USER_AGENT
from persistent_rate import PersistentLimiter, RateBlocked


def reply(status=200, content=b'{"ok":true}', headers=()):
    return SimpleNamespace(status_code=status, content=content, reason='OK', headers=Headers(headers))


class BrowserTests(unittest.TestCase):
    def test_redirect_cookie_policy_and_every_hop_counted(self):
        with tempfile.TemporaryDirectory() as directory, patch('browser_transport.curl_requests.Session') as factory:
            transport = factory.return_value
            transport.request.side_effect = [reply(302, headers=[('Location','https://www.instagram.com/next'),('Set-Cookie','csrftoken=rotated; Domain=.instagram.com; Path=/; Secure'),('Set-Cookie','invalid=no; Domain=evil.test; Path=/')]), reply()]
            session=requests.Session();session.cookies.set('sessionid','private',domain='.instagram.com',path='/')
            limiter=PersistentLimiter(Path(directory),sleep=lambda _:None)
            with limiter.transport(), browser_transport():
                result=session.get('https://www.instagram.com/start',timeout=9,proxies={'https':'http://proxy.test:8080'})
            self.assertEqual(result.json(),{'ok':True})
            self.assertEqual(len(limiter.state['requests']),2)
            self.assertEqual(session.cookies.get('csrftoken'),'rotated')
            self.assertIsNone(session.cookies.get('invalid'))
            self.assertEqual(len(result.history),1)
            second=transport.request.call_args_list[1].kwargs
            self.assertIn('csrftoken=rotated',second['headers']['Cookie'])
            self.assertEqual(second['headers']['User-Agent'],USER_AGENT)
            self.assertEqual(second['proxy'],'http://proxy.test:8080')
            self.assertEqual(second['timeout'],9)
            self.assertFalse(second['allow_redirects'])
            transport.close.assert_called_once()

    def test_cooldown_no_curl_or_requests_fallback(self):
        with tempfile.TemporaryDirectory() as directory, patch('browser_transport.curl_requests.Session') as factory:
            transport=factory.return_value
            transport.request.return_value=reply(429,headers=[('Retry-After','1800')])
            limiter=PersistentLimiter(Path(directory))
            with limiter.transport(), browser_transport():
                with self.assertRaises(RateBlocked):requests.Session().get('https://www.instagram.com/test')
            second=PersistentLimiter(Path(directory))
            with second.transport(), browser_transport():
                with self.assertRaises(RateBlocked):requests.Session().get('https://www.instagram.com/test')
            self.assertEqual(transport.request.call_count,1)
            self.assertEqual(len(second.state['requests']),1)

    def test_baseline_temporarily_uses_original_transport(self):
        session=requests.Session();original=session.get_adapter('https://www.instagram.com/')
        with browser_transport():
            curl=session.get_adapter('https://www.instagram.com/')
            self.assertIsNot(curl,original)
            with browser_transport(False):self.assertIs(session.get_adapter('https://www.instagram.com/'),original)
            self.assertIs(session.get_adapter('https://www.instagram.com/'),curl)

    def test_non_instagram_keeps_original_adapter(self):
        session=requests.Session();old=session.get_adapter('https://example.com/')
        with browser_transport():
            self.assertIs(session.get_adapter('https://example.com/'),old)
            self.assertIs(session.get_adapter('https://instagram.com.evil.test/'),old)

    def test_post_body_and_timeout_remain_requests_compatible(self):
        from curl_cffi.requests.exceptions import Timeout
        with patch('browser_transport.curl_requests.Session') as factory:
            transport=factory.return_value;transport.request.return_value=reply()
            with browser_transport():
                requests.Session().post('https://www.instagram.com/test',data={'a':'b c'},timeout=(3,7))
                self.assertEqual(transport.request.call_args.kwargs['data'],'a=b+c')
                self.assertEqual(transport.request.call_args.kwargs['timeout'],(3,7))
                transport.request.side_effect=Timeout('not exposed')
                with self.assertRaises(requests.exceptions.Timeout):requests.Session().get('https://www.instagram.com/test')

if __name__=='__main__':unittest.main()
