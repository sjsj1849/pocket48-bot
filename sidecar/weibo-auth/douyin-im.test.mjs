import assert from 'node:assert/strict';
import test from 'node:test';
import { buildDouyinIMWebSocketURL, decodeDouyinIMInit, decodeDouyinIMPush, isOwnDouyinIMMessage } from './douyin-im.mjs';

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
    conversationShortId: '', messageType: 7, senderUid: '12345', senderSecUid: '', senderNameHint: '', createTime: 1784251775000,
    quotedName: '', quotedText: '', quotedSenderUid: '', internalMetadata: false, contentKeys: ['text'],
    text: '只读测试', link: '',
  });
});

test('decodes reply context and answer text', () => {
  const content = JSON.stringify({ text: '回复内容', reply_info: { nickname: '原发送人', text: '原消息', sender_uid: '123' } });
  const message = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 8), ...intField(6, 7), ...intField(7, 456), ...field(8, content)];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '回复内容');
  assert.equal(decoded.quotedName, '原发送人');
  assert.equal(decoded.quotedText, '原消息');
  assert.equal(decoded.quotedSenderUid, '123');
});

test('uses the sender nickname carried in message ext', () => {
  const content = JSON.stringify({ text: '你好' });
  const senderNickname = [...field(1, 's:sender_nickname'), ...field(2, '测试昵称')];
  const message = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 9), ...intField(6, 7), ...intField(7, 456), ...field(8, content), ...field(9, senderNickname)];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  assert.equal(decodeDouyinIMPush(frame).senderNameHint, '测试昵称');
});

test('normalizes favorite emoji controls and marks their metadata frame', () => {
  const emojiContent = JSON.stringify({ content: 'favorite_emoji' });
  const emojiMessage = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 10), ...intField(6, 2001), ...intField(7, 456), ...field(8, emojiContent)];
  const emojiNotify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, emojiMessage)];
  const emojiFrame = [...field(7, 'pb'), ...field(8, field(6, field(500, emojiNotify)))];
  assert.equal(decodeDouyinIMPush(emojiFrame).text, '[表情]');

  const metadataContent = JSON.stringify({ content: '0:1:456:123' });
  const metadataMessage = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 11), ...intField(6, 2001), ...intField(7, 456), ...field(8, metadataContent)];
  const metadataNotify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, metadataMessage)];
  const metadataFrame = [...field(7, 'pb'), ...field(8, field(6, field(500, metadataNotify)))];
  assert.equal(decodeDouyinIMPush(metadataFrame).internalMetadata, true);
});

test('builds a signed read-only websocket URL', () => {
  const url = new URL(buildDouyinIMWebSocketURL('12345'));
  assert.equal(url.hostname, 'frontier-im.douyin.com');
  assert.equal(url.searchParams.get('device_id'), '12345');
  assert.match(url.searchParams.get('access_key'), /^[a-f0-9]{32}$/);
});

test('identifies outgoing messages by the persisted self uid', () => {
  assert.equal(isOwnDouyinIMMessage('1249505333755464', '1249505333755464'), true);
  assert.equal(isOwnDouyinIMMessage('peer', '1249505333755464'), false);
  assert.equal(isOwnDouyinIMMessage('peer', ''), false);
});
