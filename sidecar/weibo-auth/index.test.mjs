import test from 'node:test';
import assert from 'node:assert/strict';

import { formatCookies, parseCookieHeader } from './cookies.mjs';

test('parseCookieHeader preserves values containing equals signs', () => {
  assert.deepEqual(
    [...parseCookieHeader('SUB=abc==; XSRF-TOKEN=token').entries()],
    [['SUB', 'abc=='], ['XSRF-TOKEN', 'token']],
  );
});

test('formatCookies deduplicates and sorts cookies', () => {
  assert.equal(formatCookies([
    { name: 'SUB', value: 'old' },
    { name: 'A', value: '1' },
    { name: 'SUB', value: 'new' },
  ]), 'A=1; SUB=new');
});
