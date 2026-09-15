"""Requests adapter using a stable browser TLS identity.

Inspired by instagram_monitor's optional curl_cffi transport. Requests still owns
cookie scoping, request preparation and redirects; there are no hidden retries.
"""
import contextlib
import io
from http.client import HTTPMessage
from types import SimpleNamespace
from urllib.parse import urlparse

import requests
from requests.adapters import HTTPAdapter
from urllib3.response import HTTPResponse
from urllib3._collections import HTTPHeaderDict
from curl_cffi import requests as curl_requests

ORIGINAL_ADAPTER = requests.Session.get_adapter
BROWSER = 'chrome136'
USER_AGENT = ('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 '
              '(KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36')


def instagram_url(url):
    host = urlparse(url).hostname or ''
    return urlparse(url).scheme == 'https' and (host == 'instagram.com' or host.endswith('.instagram.com'))


class BrowserAdapter(HTTPAdapter):
    def __init__(self):
        super().__init__(max_retries=0)
        # Requests is the only cookie jar. Discard libcurl's independent cookies.
        self.transport = curl_requests.Session(trust_env=False, discard_cookies=True)

    def send(self, request, stream=False, timeout=None, verify=True, cert=None, proxies=None):
        headers = dict(request.headers)
        headers['User-Agent'] = USER_AGENT
        for key in list(headers):
            if key.lower().startswith('sec-ch-ua'):
                del headers[key]
        try:
            reply = self.transport.request(
                request.method, request.url, data=request.body,
                headers=headers, impersonate=BROWSER, default_headers=True,
                timeout=timeout if timeout is not None else 20,
                proxy=requests.utils.select_proxy(request.url, proxies or {}),
                verify=verify, cert=cert, allow_redirects=False,
            )
        except curl_requests.exceptions.Timeout as error:
            raise requests.exceptions.Timeout('Instagram browser transport timeout', request=request) from error
        except curl_requests.exceptions.RequestException as error:
            raise requests.exceptions.ConnectionError('Instagram browser transport failed', request=request) from error
        # Preserve duplicate Set-Cookie headers for Requests' domain/path policy.
        message = HTTPMessage()
        raw_headers = HTTPHeaderDict()
        for key, value in reply.headers.multi_items():
            message.add_header(key, value)
            raw_headers.add(key, value)
        raw = HTTPResponse(body=io.BytesIO(reply.content), headers=raw_headers,
                           status=reply.status_code, reason=reply.reason,
                           preload_content=False, decode_content=False)
        raw._original_response = SimpleNamespace(msg=message)
        response = self.build_response(request, raw)
        # libcurl has already decoded compressed bytes.
        response._content = reply.content
        response._content_consumed = True
        return response

    def close(self):
        self.transport.close()
        super().close()


@contextlib.contextmanager
def browser_transport(enabled=True):
    if not enabled:
        previous = requests.Session.get_adapter
        requests.Session.get_adapter = ORIGINAL_ADAPTER
        try:
            yield
        finally:
            requests.Session.get_adapter = previous
        return
    original = requests.Session.get_adapter
    adapter = BrowserAdapter()

    def select(session, url):
        return adapter if instagram_url(url) else original(session, url)

    requests.Session.get_adapter = select
    try:
        yield
    finally:
        requests.Session.get_adapter = original
        adapter.close()
