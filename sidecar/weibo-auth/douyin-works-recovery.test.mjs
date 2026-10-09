import test from 'node:test';
import assert from 'node:assert/strict';
import {
  DOUYIN_WORKS_BATCH_SIZE,
  DOUYIN_WORKS_API_RETRY_MS,
  DouyinWorksAccountRotation,
  DouyinWorksFallbackRotation,
} from './douyin-works-recovery.mjs';

test('retries the lightweight works API after the 5-minute cooldown', () => {
  assert.equal(DOUYIN_WORKS_API_RETRY_MS, 5 * 60_000);
});

test('rotates exactly one valid creator for each fallback poll', () => {
  const rotation = new DouyinWorksFallbackRotation();
  const accounts = [
    { secUserId: 'creator-a' },
    { secUserId: '' },
    { secUserId: 'creator-b' },
    { name: 'missing-id' },
    { secUserId: 'creator-c' },
  ];
  assert.deepEqual([
    rotation.next(accounts),
    rotation.next(accounts),
    rotation.next(accounts),
    rotation.next(accounts),
  ], ['creator-a', 'creator-b', 'creator-c', 'creator-a']);
  assert.equal(rotation.next([]), '');
});

test('spreads fast works polling across small rotating batches', () => {
  assert.equal(DOUYIN_WORKS_BATCH_SIZE, 2);
  const rotation = new DouyinWorksAccountRotation();
  const accounts = ['a', 'b', 'c', 'd', 'e', 'f'].map((secUserId) => ({ secUserId }));
  assert.deepEqual(rotation.take(accounts).map((account) => account.secUserId), ['a', 'b']);
  assert.deepEqual(rotation.take(accounts).map((account) => account.secUserId), ['c', 'd']);
  assert.deepEqual(rotation.take(accounts).map((account) => account.secUserId), ['e', 'f']);
  assert.deepEqual(rotation.take(accounts).map((account) => account.secUserId), ['a', 'b']);
});
