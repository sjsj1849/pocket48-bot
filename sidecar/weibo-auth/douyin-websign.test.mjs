import test from 'node:test';
import assert from 'node:assert/strict';
import { signDouyinProtectedParams } from './douyin-websign.mjs';

test('reproduces the Douyin SDK web-signature vector', () => {
  const params = new URLSearchParams('device_platform=webapp&aid=6383&aweme_id=123');
  const signed = signDouyinProtectedParams(params, {
    UIFID_TEMP: 'aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff0011',
  }, 1788848841);
  assert.equal(signed.signature, '6090b6162b96aeb107b51ab69140c764');
  assert.equal(signed.headers['x-secsdk-web-expire'], '1788848841');
});

test('binds the protected signature to the browser visitor cookies', () => {
  const signed = signDouyinProtectedParams(new URLSearchParams('a=1'), {
    UIFID: 'visitor-id',
    UIFID_TEMP: 'lower-priority-id',
    s_v_web_id: 'verify-id',
  }, 123);
  assert.match(signed.query, /a=1&verifyFp=verify-id&fp=verify-id&uifid=visitor-id&timestamp=123/);
  assert.equal(signed.headers.uifid, 'visitor-id');
  assert.equal(signed.headers['x-secsdk-web-signature'], signed.signature);
});

test('does not invent a visitor identity', () => {
  const signed = signDouyinProtectedParams(new URLSearchParams('a=1'), {}, 123);
  assert.equal(signed.query, 'a=1');
  assert.equal(signed.signature, '');
  assert.deepEqual(signed.headers, {});
});
