import assert from 'node:assert/strict';
import test from 'node:test';
import { buildDouyinIMWebSocketURL, decodeDouyinIMInit, decodeDouyinIMPush } from './douyin-im.mjs';

function varint(value) {
  let current = BigInt(value);
  const result = [];
  do {
    let byte = Number(current & 0x7fn);
    current >>= 7n;
    if (current) byte |= 0x80;
    result.push(byte);
  } while (current);
  return result;
}

function field(number, value) {
  const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : Uint8Array.from(value);
  return [...varint((number << 3) | 2), ...varint(bytes.length), ...bytes];
}

function intField(number, value) {
  return [...varint(number << 3), ...varint(value)];
}

test('decodes the configured group owner from the IM init envelope', () => {
  const groupNumber = [...field(1, 'a:s_group_number'), ...field(2, '123456789000')];
  const core = [...field(1, 'group-conv'), ...intField(2, 88), ...intField(3, 2), ...field(5, '目标群'), ...field(11, groupNumber), ...intField(12, 12345)];
  const conversation = [...field(1, 'group-conv'), ...intField(3, 2), ...field(50, core)];
  const wrapper = field(1, conversation);
  const init = field(1, wrapper);
  const body = field(2043, init);
  const envelope = [...field(6, body), ...intField(13, 99999)];
  assert.deepEqual(decodeDouyinIMInit(envelope), {
    selfUid: '99999',
    groups: [{ conversationId: 'group-conv', conversationShortId: '88', name: '目标群', ownerUid: '12345', ownerSecUid: '', groupNumber: '123456789000' }],
  });
});

test('decodes a text push with its server creation time', () => {
  const content = JSON.stringify({ text: '只读测试' });
  const message = [...field(1, 'group-conv'), ...intField(2, 2), ...intField(3, 7), ...intField(4, 9), ...intField(6, 7), ...intField(7, 12345), ...field(8, content), ...intField(10, 1784251775000)];
  const notify = [...field(2, 'group-conv'), ...intField(3, 2), ...field(5, message)];
  const body = field(500, notify);
  const response = field(6, body);
  const frame = [...field(7, 'pb'), ...field(8, response)];
  assert.deepEqual(decodeDouyinIMPush(frame), {
    conversationId: 'group-conv', conversationType: 2, serverMessageId: '7', index: '9',
    conversationShortId: '', messageType: 7, senderUid: '12345', senderSecUid: '', createTime: 1784251775000,
    text: '只读测试', link: '',
  });
});

test('builds a signed read-only websocket URL', () => {
  const url = new URL(buildDouyinIMWebSocketURL('12345'));
  assert.equal(url.hostname, 'frontier-im.douyin.com');
  assert.equal(url.searchParams.get('device_id'), '12345');
  assert.match(url.searchParams.get('access_key'), /^[a-f0-9]{32}$/);
});
