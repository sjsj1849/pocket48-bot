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

test('hard timeout runs cancellation cleanup before rejecting', async () => {
  const calls = [];
  await assert.rejects(
    withHardTimeout(new Promise(() => {}), 10, 'creator scan', async () => {
      calls.push('cleanup-start');
      await new Promise((resolve) => setTimeout(resolve, 5));
      calls.push('cleanup-end');
    }),
    /creator scan hard timeout after 10ms/,
  );
  assert.deepEqual(calls, ['cleanup-start', 'cleanup-end']);
});
