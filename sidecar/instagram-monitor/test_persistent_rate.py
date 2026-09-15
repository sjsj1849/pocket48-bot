import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch
import requests

from persistent_rate import PersistentLimiter, RateBlocked


class Clock:
    def __init__(self): self.wall, self.mono = 10000., 100.
    def sleep(self, seconds): self.wall += seconds; self.mono += seconds
    def limiter(self, path): return PersistentLimiter(path, now=lambda:self.wall, monotonic=lambda:self.mono, sleep=self.sleep)


class RateTests(unittest.TestCase):
    def test_http_counts_survive_new_workers(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory); clock = Clock()
            clock.limiter(path).reserve_http()
            second = clock.limiter(path); second.reserve_http()
            state = json.loads((path / 'request-state.json').read_text())
            self.assertEqual(len(state['requests']), 2)
            self.assertEqual(state['requests'][1] - state['requests'][0], 2)
            self.assertEqual((path / 'request-state.json').stat().st_mode & 0o777, 0o600)

    def test_builtin_timestamps_survive_monotonic_reset(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory);clock=Clock();ctx=SimpleNamespace()
            clock.limiter(path).controller(ctx).wait_before_query('other')
            clock.wall += 5;clock.mono = 1
            controller=clock.limiter(path).controller(ctx)
            self.assertEqual(controller._query_timestamps['other'], [-4])
            controller.wait_before_query('other')
            self.assertEqual(len(clock.limiter(path).state['builtin']['other']), 2)

    def test_server_cooldown_survives_and_sends_no_more_http(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory);clock=Clock();response=requests.Response();response.status_code=401
            response._content=b'{"status":"fail","message":"Please wait a few minutes before you try again."}'
            with patch.object(requests.Session,'send',return_value=response) as network:
                first=clock.limiter(path)
                with first.transport():
                    with self.assertRaises(RateBlocked): requests.Session().get('https://www.instagram.com/graphql/query')
                second=clock.limiter(path)
                with second.transport():
                    with self.assertRaises(RateBlocked): requests.Session().get('https://www.instagram.com/api/v1/feed/user/42/')
                self.assertEqual(network.call_count,1)
            self.assertEqual(len(second.state['requests']),1)
            clock.wall += 901
            with self.assertRaises(RateBlocked) as blocked: clock.limiter(path).server_limit()
            self.assertGreaterEqual(blocked.exception.until-clock.wall,1800)

    def test_local_budget_is_shared(self):
        with tempfile.TemporaryDirectory() as directory:
            path=Path(directory);clock=Clock()
            for _ in range(12): clock.limiter(path).reserve_http()
            with self.assertRaises(RateBlocked) as blocked: clock.limiter(path).reserve_http()
            self.assertEqual(blocked.exception.reason,'request_budget')
            self.assertEqual(len(clock.limiter(path).state['requests']),12)

if __name__ == '__main__': unittest.main()
