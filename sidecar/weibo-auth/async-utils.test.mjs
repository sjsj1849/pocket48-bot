import test from 'node:test';
import assert from 'node:assert/strict';
import { withHardTimeout } from './async-utils.mjs';

test('hard timeout releases a scan that never settles', async () => {
  await assert.rejects(
    withHardTimeout(new Promise(() => {}), 10, 'creator scan'),
    /creator scan hard timeout after 10ms/,
  );
});

test('hard timeout preserves a completed scan result', async () => {
  assert.equal(await withHardTimeout(Promise.resolve('ok'), 100, 'creator scan'), 'ok');
});
