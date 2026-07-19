import assert from 'node:assert/strict';
import test from 'node:test';
import { gzipSync } from 'node:zlib';
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

function mapEntry(key, value) {
  return field(1, key).concat(field(2, value));
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
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.conversationId, 'group-conv');
  assert.equal(decoded.conversationType, 2);
  assert.equal(decoded.serverMessageId, '7');
  assert.equal(decoded.index, '9');
  assert.equal(decoded.messageType, 7);
  assert.equal(decoded.senderUid, '12345');
  assert.equal(decoded.createTime, 1784251775000);
  assert.equal(decoded.internalMetadata, false);
  assert.deepEqual(decoded.contentKeys, ['text']);
  assert.equal(decoded.text, '只读测试');
  assert.equal(decoded.link, '');
  assert.equal(decoded.contentParseOk, true);
  assert.ok(decoded.contentLen > 0);
  assert.match(decoded.contentPreview, /只读测试/);
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

test('decodes type 77 aweme share cards as video instead of author nickname', () => {
  // Real card logs: content_name=author, content_title=title?, itemId=..., cover_url=...
  // Regression: firstText used to pick content_name ("仁爱路") as plain chat text.
  const content = JSON.stringify({
    aweType: 0,
    awemeType: 0,
    content_name: '仁爱路',
    content_thumb: 'https://example.com/thumb.jpg',
    content_title: '一条测试视频标题',
    cover_url: 'https://example.com/cover.jpg',
    cover_width: 720,
    cover_height: 1280,
    itemId: '7123456789012345678',
    is_card: true,
  });
  const message = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 20), ...intField(6, 77), ...intField(7, 456), ...field(8, content)];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '[视频] 一条测试视频标题');
  assert.equal(decoded.link, 'https://www.douyin.com/video/7123456789012345678');
  assert.notEqual(decoded.text, '仁爱路');

  // Title-less share still becomes [视频], never the author name.
  const bare = JSON.stringify({
    content_name: '仁爱路',
    cover_url: 'https://example.com/cover.jpg',
    itemId: '7987654321098765432',
  });
  const bareMessage = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 21), ...intField(6, 77), ...intField(7, 456), ...field(8, bare)];
  const bareNotify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, bareMessage)];
  const bareFrame = [...field(7, 'pb'), ...field(8, field(6, field(500, bareNotify)))];
  assert.equal(decodeDouyinIMPush(bareFrame).text, '[视频]');
  assert.equal(decodeDouyinIMPush(bareFrame).link, 'https://www.douyin.com/video/7987654321098765432');
});

test('parses light interaction type 50002 from ext payload', () => {
  // Minimal synthetic message: type=50002, empty content text, sticker name in ext.
  const content = JSON.stringify({});
  const light = JSON.stringify({ name: '早点睡', text: '早点睡' });
  const message = [
    ...field(1, '7660087481085248037'),
    ...intField(2, 2n),
    ...field(3, 'msg-light-1'),
    ...field(4, '1'),
    ...field(5, 'short-1'),
    ...intField(6, 50002n),
    ...field(7, '1643123816276426'),
    ...field(8, content),
    ...field(9, mapEntry('a:light_interaction', light)),
    ...intField(10, 1784302422000n),
  ];
  const notify = [...field(2, '7660087481085248037'), ...intField(3, 2n), ...field(5, message)];
  const body = field(500, notify);
  const payload = field(6, body);
  const frame = [
    ...field(6, 'gzip'),
    ...field(7, 'pb'),
    ...field(8, gzipSync(Buffer.from(payload))),
  ];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.messageType, 50002);
  assert.equal(decoded.text, '[早点睡]');
  assert.equal(decoded.senderUid, '1643123816276426');
});

test('wraps named Douyin stickers so they are not mistaken for chat text', () => {
  const content = JSON.stringify({ text: '早点睡' });
  const message = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 12), ...intField(6, 7), ...intField(7, 456), ...field(8, content)];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  assert.equal(decodeDouyinIMPush(frame).text, '[早点睡]');

  const type5 = JSON.stringify({ text: '微笑' });
  const message5 = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 13), ...intField(6, 5), ...intField(7, 456), ...field(8, type5)];
  const notify5 = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message5)];
  const frame5 = [...field(7, 'pb'), ...field(8, field(6, field(500, notify5)))];
  assert.equal(decodeDouyinIMPush(frame5).text, '[微笑]');

  // Ordinary chat text must stay plain.
  const chat = JSON.stringify({ text: '早点睡吧明天还要早起' });
  const chatMessage = [...field(1, 'private-conv'), ...intField(2, 1), ...intField(3, 14), ...intField(6, 7), ...intField(7, 456), ...field(8, chat)];
  const chatNotify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, chatMessage)];
  const chatFrame = [...field(7, 'pb'), ...field(8, field(6, field(500, chatNotify)))];
  assert.equal(decodeDouyinIMPush(chatFrame).text, '早点睡吧明天还要早起');
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

test('type 1 system notice (go_to_maya_notice) is internal and not chat text', () => {
  // Prod 2026-07-18 陆苇: type=1 content={aweType,scene,tips,template,show_on_screen,...}
  // Old firstText walked scene → leaked "go_to_maya_notice" to QQ.
  const content = JSON.stringify({
    aweType: 0,
    scene: 'go_to_maya_notice',
    tips: '',
    template: '',
    show_on_screen: true,
    ios_filter_min_version: '0',
    ios_filter_max_version: '999',
  });
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 30),
    ...intField(6, 1),
    ...intField(7, 4309688567216519n),
    ...field(8, content),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.messageType, 1);
  assert.equal(decoded.internalMetadata, true);
  assert.notEqual(decoded.text, 'go_to_maya_notice');
  assert.match(decoded.text, /系统提示|go_to_maya_notice/);
});

test('firstText does not treat random object values as chat body', () => {
  // Regression: walking Object.values picked scene=go_to_maya_notice as text.
  const content = JSON.stringify({
    scene: 'go_to_maya_notice',
    tips: '',
    template: '',
    show_on_screen: 1,
  });
  // Use type 7 so we hit the generic firstText path (not type-1 system branch).
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 31),
    ...intField(6, 7),
    ...intField(7, 456),
    ...field(8, content),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  // System-notice shape is caught even without type=1.
  assert.equal(decoded.internalMetadata, true);
  assert.notEqual(decoded.text, 'go_to_maya_notice');
});

test('reply message still extracts body + quoted text when content has reply_info', () => {
  const content = JSON.stringify({
    text: '这是回复',
    reply_info: { nickname: '我', text: '原消息', sender_uid: '1' },
  });
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 32),
    ...intField(6, 7),
    ...intField(7, 3465253341373239n),
    ...field(8, content),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '这是回复');
  assert.equal(decoded.quotedName, '我');
  assert.equal(decoded.quotedText, '原消息');
  assert.equal(decoded.internalMetadata, false);
});

test('reply via reference_info + ref server id', () => {
  const content = JSON.stringify({
    text: '诱惑你快点去看小肥发了啥',
    reference_info: {
      text: '我发的内容',
      nickname: '我',
      sender_uid: '1249505333755464',
      server_message_id: '999001',
    },
  });
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 40),
    ...intField(6, 7),
    ...intField(7, 3465253341373239n),
    ...field(8, content),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '诱惑你快点去看小肥发了啥');
  assert.equal(decoded.quotedText, '我发的内容');
  assert.equal(decoded.quotedName, '我');
  assert.equal(decoded.quotedServerMessageId, '999001');
});

test('reply ref id only in ext (no body copy)', () => {
  const content = JSON.stringify({ text: '诱惑你快点去看小肥发了啥' });
  const extEntries = [
    ...field(1, 's:ref_server_message_id'), ...field(2, '888001'),
  ];
  // field 9 is repeated map entries; pack one entry
  const extEntry = [...field(1, 's:ref_server_message_id'), ...field(2, '888001')];
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 41),
    ...intField(6, 7),
    ...intField(7, 3465253341373239n),
    ...field(8, content),
    ...field(9, extEntry),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '诱惑你快点去看小肥发了啥');
  assert.equal(decoded.quotedServerMessageId, '888001');
  void extEntries;
});

test('type 7 with empty content body reports empty contentPreview and unsupported text', () => {
  // Prod 2026-07-18 唐欣怡 08:17: content_keys were only ext:* — field 8 empty/{} .
  const content = JSON.stringify({});
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 33),
    ...intField(6, 7),
    ...intField(7, 3465253341373239n),
    ...field(8, content),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.messageType, 7);
  assert.equal(decoded.text, '[暂不支持的消息]');
  assert.equal(decoded.contentParseOk, true);
  assert.ok(decoded.contentLen >= 0);
});

test('recovers Chinese chat text from non-JSON field 8 bytes', () => {
  // Hypothesis for image-reply: field 8 is not {"text":...} JSON but still carries UTF-8 body.
  const body = new TextEncoder().encode('哈哈哈哈哈哈哈');
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 34),
    ...intField(6, 7),
    ...intField(7, 3465253341373239n),
    ...field(8, body),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.contentParseOk, false);
  assert.equal(decoded.text, '哈哈哈哈哈哈哈');
  assert.equal(decoded.internalMetadata, false);
});

test('recovers chat text from alternate protobuf field when field 8 is empty', () => {
  // Image-reply may leave field 8 as {} and put body JSON elsewhere.
  const empty = JSON.stringify({});
  const alt = JSON.stringify({ text: '这是要干嘛' });
  const message = [
    ...field(1, 'private-conv'),
    ...intField(2, 1),
    ...intField(3, 35),
    ...intField(6, 7),
    ...intField(7, 3465253341373239n),
    ...field(8, empty),
    ...field(15, alt),
  ];
  const notify = [...field(2, 'private-conv'), ...intField(3, 1), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '这是要干嘛');
  assert.ok(Array.isArray(decoded.fieldHits));
  assert.ok(decoded.fieldHits.some((h) => h.includes('这是要干嘛')));
});

test('decodes type 27 image messages with url_list', () => {
  const content = JSON.stringify({
    url_list: [
      'https://p3-im.byteimg.com/img/example~tplv-obj.jpeg',
      'https://p9-im.byteimg.com/img/example~tplv-obj.jpeg',
    ],
    width: 1080,
    height: 1440,
  });
  const message = [...field(1, 'group-conv'), ...intField(2, 2), ...intField(3, 27), ...intField(6, 27), ...intField(7, 12345), ...field(8, content)];
  const notify = [...field(2, 'group-conv'), ...intField(3, 2), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.messageType, 27);
  assert.equal(decoded.text, '[图片]');
  assert.deepEqual(decoded.images, [
    'https://p3-im.byteimg.com/img/example~tplv-obj.jpeg',
    'https://p9-im.byteimg.com/img/example~tplv-obj.jpeg',
  ]);
});

test('type 27 without urls still falls back to [图片]', () => {
  const content = JSON.stringify({ width: 1, height: 1 });
  const message = [...field(1, 'group-conv'), ...intField(2, 2), ...intField(3, 28), ...intField(6, 27), ...intField(7, 12345), ...field(8, content)];
  const notify = [...field(2, 'group-conv'), ...intField(3, 2), ...field(5, message)];
  const frame = [...field(7, 'pb'), ...field(8, field(6, field(500, notify)))];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.text, '[图片]');
  assert.deepEqual(decoded.images || [], []);
});

test('light interaction sticker with image url_list forwards images', () => {
  const content = JSON.stringify({});
  const light = JSON.stringify({
    name: '比心',
    text: '比心',
    url_list: ['https://p3-emoticon.byteimg.com/sticker/heart.png'],
    static_url: 'https://p3-emoticon.byteimg.com/sticker/heart.png',
  });
  const message = [
    ...field(1, '7660087481085248037'),
    ...intField(2, 2n),
    ...field(3, 'msg-sticker-img'),
    ...field(4, '1'),
    ...field(5, 'short-1'),
    ...intField(6, 50002n),
    ...field(7, '1643123816276426'),
    ...field(8, content),
    ...field(9, mapEntry('a:light_interaction', light)),
    ...intField(10, 1784302422000n),
  ];
  const notify = [...field(2, '7660087481085248037'), ...intField(3, 2n), ...field(5, message)];
  const body = field(500, notify);
  const payload = field(6, body);
  const frame = [
    ...field(6, 'gzip'),
    ...field(7, 'pb'),
    ...field(8, gzipSync(Buffer.from(payload))),
  ];
  const decoded = decodeDouyinIMPush(frame);
  assert.equal(decoded.messageType, 50002);
  assert.equal(decoded.text, '[比心]');
  assert.ok(decoded.images.includes('https://p3-emoticon.byteimg.com/sticker/heart.png'));
});

