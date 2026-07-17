import crypto from 'node:crypto';
import { gunzipSync } from 'node:zlib';

const textDecoder = new TextDecoder();

function readVarint(bytes, offset = 0) {
  let value = 0n;
  let shift = 0n;
  let cursor = offset;
  while (cursor < bytes.length) {
    const byte = BigInt(bytes[cursor]);
    cursor += 1;
    value |= (byte & 0x7fn) << shift;
    if ((byte & 0x80n) === 0n) return { value, offset: cursor };
    shift += 7n;
    if (shift > 70n) throw new Error('protobuf varint is too long');
  }
  throw new Error('truncated protobuf varint');
}

function decodeFields(input) {
  const bytes = input instanceof Uint8Array ? input : new Uint8Array(input);
  const fields = new Map();
  let offset = 0;
  while (offset < bytes.length) {
    const tag = readVarint(bytes, offset);
    offset = tag.offset;
    const number = Number(tag.value >> 3n);
    const wireType = Number(tag.value & 7n);
    if (number === 0) break;
    let value;
    if (wireType === 0) {
      const decoded = readVarint(bytes, offset);
      value = decoded.value;
      offset = decoded.offset;
    } else if (wireType === 1) {
      value = bytes.slice(offset, offset + 8);
      offset += 8;
    } else if (wireType === 2) {
      const length = readVarint(bytes, offset);
      offset = length.offset;
      const end = offset + Number(length.value);
      if (end > bytes.length) throw new Error('truncated protobuf field');
      value = bytes.slice(offset, end);
      offset = end;
    } else if (wireType === 5) {
      value = bytes.slice(offset, offset + 4);
      offset += 4;
    } else {
      throw new Error(`unsupported protobuf wire type ${wireType}`);
    }
    const list = fields.get(number) || [];
    list.push(value);
    fields.set(number, list);
  }
  return fields;
}

function first(fields, number, fallback = undefined) {
  return fields.get(number)?.[0] ?? fallback;
}

function asString(value) {
  if (value === undefined || value === null) return '';
  if (typeof value === 'bigint') return value.toString();
  try { return textDecoder.decode(value); } catch { return ''; }
}

function asNumber(value) {
  if (typeof value !== 'bigint') return 0;
  return value <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(value) : value.toString();
}

function mapValue(fields, fieldNumber, targetKey) {
  for (const entryRaw of fields.get(fieldNumber) || []) {
    const entry = decodeFields(entryRaw);
    if (asString(first(entry, 1)) === targetKey) return asString(first(entry, 2));
  }
  return '';
}

function decodeMessage(raw, fallbackConversationId = '', fallbackConversationType = 0) {
  const message = decodeFields(raw);
  const contentRaw = asString(first(message, 8));
  let content = {};
  try { content = JSON.parse(contentRaw); } catch {}
  const messageType = Number(first(message, 6, 0n));
  let text = typeof content?.text === 'string' ? content.text.trim() : '';
  let link = '';
  if (!text && messageType === 8) {
    text = `[视频]${content?.content_title ? ` ${content.content_title}` : ''}`;
    if (content?.itemId) link = `https://www.douyin.com/video/${content.itemId}`;
  }
  if (!text) {
    text = ({ 5: '[表情]', 17: '[语音]', 27: '[图片]' })[messageType] || '[暂不支持的消息]';
  }
  return {
    conversationId: asString(first(message, 1)) || fallbackConversationId,
    conversationType: Number(first(message, 2, BigInt(fallbackConversationType))),
    serverMessageId: asString(first(message, 3)),
    index: asString(first(message, 4)),
    conversationShortId: asString(first(message, 5)),
    messageType,
    senderUid: asString(first(message, 7)),
    senderSecUid: asString(first(message, 14)),
    createTime: asNumber(first(message, 10, 0n)),
    text,
    link,
  };
}

export function decodeDouyinIMPush(input) {
  const frame = decodeFields(input);
  let payload = first(frame, 8);
  if (!(payload instanceof Uint8Array)) return null;
  const encoding = asString(first(frame, 6));
  if (encoding === 'gzip' || (payload[0] === 0x1f && payload[1] === 0x8b)) {
    payload = new Uint8Array(gunzipSync(payload));
  }
  const payloadType = asString(first(frame, 7));
  if (payloadType && payloadType !== 'pb') return null;
  const response = decodeFields(payload);
  const bodyRaw = first(response, 6);
  if (!(bodyRaw instanceof Uint8Array)) return null;
  const body = decodeFields(bodyRaw);
  const notifyRaw = first(body, 500);
  if (!(notifyRaw instanceof Uint8Array)) return null;
  const notify = decodeFields(notifyRaw);
  const messageRaw = first(notify, 5);
  if (!(messageRaw instanceof Uint8Array)) return null;
  return decodeMessage(
    messageRaw,
    asString(first(notify, 2)),
    Number(first(notify, 3, 0n)),
  );
}

export function decodeDouyinIMInit(input) {
  const envelope = decodeFields(input);
  const selfUid = asString(first(envelope, 13));
  const bodyRaw = first(envelope, 6);
  if (!(bodyRaw instanceof Uint8Array)) return { selfUid, groups: [] };
  const body = decodeFields(bodyRaw);
  const initRaw = first(body, 2043);
  if (!(initRaw instanceof Uint8Array)) return { selfUid, groups: [] };
  const init = decodeFields(initRaw);
  const groups = [];
  for (const wrapperRaw of init.get(1) || []) {
    const wrapper = decodeFields(wrapperRaw);
    const conversationRaw = first(wrapper, 1);
    if (!(conversationRaw instanceof Uint8Array)) continue;
    const conversation = decodeFields(conversationRaw);
    if (Number(first(conversation, 3, 0n)) !== 2) continue;
    const coreRaw = first(conversation, 50);
    if (!(coreRaw instanceof Uint8Array)) continue;
    const core = decodeFields(coreRaw);
    groups.push({
      conversationId: asString(first(core, 1)) || asString(first(conversation, 1)),
      conversationShortId: asString(first(core, 2)) || asString(first(conversation, 2)),
      name: asString(first(core, 5)),
      ownerUid: asString(first(core, 12)),
      ownerSecUid: asString(first(core, 13)),
      groupNumber: mapValue(core, 11, 'a:s_group_number'),
    });
  }
  return { selfUid, groups };
}

export function buildDouyinIMWebSocketURL(selfUid) {
  const uid = String(selfUid || '').trim();
  if (!/^\d+$/.test(uid)) throw new Error('Douyin IM requires a numeric self uid');
  const accessKey = crypto.createHash('md5')
    .update(`9e1bd35ec9db7b8d846de66ed140b1ad9${uid}f8a69f1719916z`)
    .digest('hex');
  const params = new URLSearchParams({
    aid: '6383', fpid: '9', device_id: uid, access_key: accessKey,
    device_platform: 'douyin_pc', version_code: '360000',
    xsack: '0', xaack: '0', xsqos: '0', qos_sdk_version: '2',
  });
  return `wss://frontier-im.douyin.com/ws/v2?${params}`;
}

export const douyinIMInternals = { decodeFields, asString, asNumber };
