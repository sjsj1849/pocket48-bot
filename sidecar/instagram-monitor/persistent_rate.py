"""Account-wide rate state, protected by the collector's session.lock.

Epoch timestamps survive new processes and host restarts. HTTP calls are reserved
before sending so a killed worker cannot erase its request budget.
"""
import contextlib
import datetime
import json
import os
import tempfile
import time
from urllib.parse import urlparse

import instaloader
import requests


class RateBlocked(Exception):
    code = 'cooldown'

    def __init__(self, until, reason):
        self.until = until
        self.reason = reason
        self.retry_at = datetime.datetime.fromtimestamp(until, datetime.timezone.utc).isoformat()


def atomic_write(path, data):
    fd, temp = tempfile.mkstemp(dir=path.parent, prefix='.rate-')
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(data, f)
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


class PersistentLimiter:
    def __init__(self, directory, now=time.time, monotonic=time.monotonic, sleep=time.sleep):
        self.path = directory / 'request-state.json'
        self.now, self.monotonic, self.sleep = now, monotonic, sleep
        self.state = json.loads(self.path.read_text()) if self.path.exists() else {}
        self.state.setdefault('requests', [])
        self.state.setdefault('builtin', {})
        self.state['requests'] = [t for t in self.state['requests'] if t > self.now() - 3600]

    def save(self):
        self.state['updatedAt'] = self.now()
        atomic_write(self.path, self.state)

    def freeze(self, until, reason, server=False):
        self.state['blockedUntil'] = max(until, self.state.get('blockedUntil', 0))
        self.state['reason'] = reason
        if server:
            self.state['failureStreak'] = min(6, self.state.get('failureStreak', 0) + 1)
        self.save()
        raise RateBlocked(self.state['blockedUntil'], reason)

    def gate(self):
        until = self.state.get('blockedUntil', 0)
        if until > self.now():
            raise RateBlocked(until, self.state.get('reason', 'rate_limit'))

    def reserve_http(self):
        self.gate()
        now = self.now()
        stamps = [t for t in self.state['requests'] if t > now - 3600]
        waits = []
        for window, maximum in ((60, 12), (600, 60)):
            recent = [t for t in stamps if t > now - window]
            if len(recent) >= maximum:
                waits.append(min(recent) + window + 1)
        if waits:
            self.freeze(max(waits), 'request_budget')
        # Keep requests spaced across operations, including browser validation.
        delay = max(0, (stamps[-1] + 2 if stamps else 0) - now)
        if delay:
            self.sleep(min(delay, 2))
        self.state['requests'] = stamps + [self.now()]
        self.save()

    def server_limit(self, retry_after=None):
        streak = self.state.get('failureStreak', 0)
        seconds = min(21600, 900 * (2 ** min(streak, 4)))
        try:
            seconds = max(seconds, min(21600, int(retry_after)))
        except (TypeError, ValueError):
            pass
        self.freeze(self.now() + seconds, 'rate_limit', server=True)

    def success(self):
        self.gate()
        self.state['failureStreak'] = 0
        self.state['reason'] = ''
        self.save()

    def controller(self, context):
        limiter = self

        class Controller(instaloader.RateController):
            def __init__(self, ctx):
                super().__init__(ctx)
                mono, wall = limiter.monotonic(), limiter.now()
                self._query_timestamps = {k: [mono + t - wall for t in v if t > wall - 3600]
                                          for k, v in limiter.state['builtin'].items()}
                self._earliest_next_request_time = 0
                self._iphone_earliest_next_request_time = 0

            def wait_before_query(self, query_type):
                limiter.gate()
                wait = self.query_waittime(query_type, limiter.monotonic(), False)
                if wait > 0:
                    limiter.freeze(limiter.now() + wait, 'request_budget')
                self._query_timestamps.setdefault(query_type, []).append(limiter.monotonic())
                mono, wall = limiter.monotonic(), limiter.now()
                limiter.state['builtin'] = {k: [wall + t - mono for t in v if t > mono - 3600]
                                            for k, v in self._query_timestamps.items()}
                limiter.save()

            def handle_429(self, query_type):
                limiter.server_limit()

        return Controller(context)

    @contextlib.contextmanager
    def transport(self):
        original = requests.Session.request
        limiter = self

        def counted(session, method, url, *args, **kwargs):
            host = urlparse(url).hostname or ''
            is_instagram = host == 'instagram.com' or host.endswith('.instagram.com')
            if is_instagram:
                limiter.reserve_http()
            response = original(session, method, url, *args, **kwargs)
            if is_instagram:
                limited = response.status_code == 429
                if response.status_code in (401, 403):
                    limited = b'wait a few minutes' in response.content[:65536].lower()
                if limited:
                    limiter.server_limit(response.headers.get('Retry-After'))
            return response

        requests.Session.request = counted
        try:
            yield
        finally:
            requests.Session.request = original
