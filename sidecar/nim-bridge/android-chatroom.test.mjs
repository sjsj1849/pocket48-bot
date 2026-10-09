import assert from 'node:assert/strict';
import test from 'node:test';

import { extractOnlineMemberNum } from './android-chatroom.mjs';

function varint(value) {
  const bytes = [];
  do {
    let byte = value & 0x7f;
    value = Math.floor(value / 128);
    if (value) byte |= 0x80;
    bytes.push(byte);
  } while (value);
  return Buffer.from(bytes);
}

function properties(entries) {
  const parts = [varint(entries.length)];
  for (const [key, value] of entries) {
    const body = Buffer.isBuffer(value) ? value : Buffer.from(String(value));
    parts.push(varint(key), varint(body.length), body);
  }
  return Buffer.concat(parts);
}

test('extracts direct chatroom online member count', () => {
  assert.equal(extractOnlineMemberNum(properties([[101, '37']])), 37);
});

test('extracts nested chatroom online member count', () => {
  const nested = properties([[101, '128']]);
  assert.equal(extractOnlineMemberNum(properties([[1, nested]])), 128);
});

test('does not mistake unrelated numeric fields for online count', () => {
  assert.equal(extractOnlineMemberNum(properties([[1, '999']])), undefined);
});
