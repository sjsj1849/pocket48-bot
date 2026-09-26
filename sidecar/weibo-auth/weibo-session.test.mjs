import test from 'node:test';
import assert from 'node:assert/strict';

import { classifyWeiboProbe, summarizeWeiboProbes } from './weibo-session.mjs';

test('cookie presence cannot turn a redirected session into healthy', () => {
  const result = classifyWeiboProbe('web', {
    status: 200,
    url: 'https://passport.weibo.com/visitor/visitor',
    body: { ok: 1 },
  });
  assert.equal(result.state, 'invalid');
});

test('requires authenticated API responses', () => {
  assert.equal(classifyWeiboProbe('web', { status: 200, url: 'https://weibo.com/ajax/config/get_config', body: { ok: 1 } }).state, 'valid');
  assert.equal(classifyWeiboProbe('mobile', { status: 200, url: 'https://m.weibo.cn/api/config', body: { data: { login: false } } }).state, 'invalid');
});

test('distinguishes network failure from login expiry', () => {
  assert.equal(summarizeWeiboProbes({ state: 'unreachable' }, { state: 'unreachable' }), 'network_error');
  assert.equal(summarizeWeiboProbes({ state: 'invalid' }, { state: 'unreachable' }), 'login_required');
  assert.equal(summarizeWeiboProbes({ state: 'valid' }, { state: 'invalid' }), 'partial');
});
