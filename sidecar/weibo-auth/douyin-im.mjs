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

function mapEntries(fields, fieldNumber) {
  const result = {};
  for (const entryRaw of fields.get(fieldNumber) || []) {
    try {
      const entry = decodeFields(entryRaw);
      const key = asString(first(entry, 1));
      if (key) result[key] = asString(first(entry, 2));
    } catch {}
  }
  return result;
}

// Only return strings found under explicit keys. Recursing into Object.values and
// accepting any string used to surface machine fields like scene=go_to_maya_notice
// as if they were chat text.
function firstText(value, keys, depth = 0) {
  if (depth > 4 || value == null) return '';
  if (typeof value === 'string') {
    const text = value.trim();
    if (!text) return '';
    if ((text.startsWith('{') && text.endsWith('}')) || (text.startsWith('[') && text.endsWith(']'))) {
      try { return firstText(JSON.parse(text), keys, depth + 1); } catch {}
    }
    // Bare string only when the caller passed a string root (depth 0).
    return depth === 0 ? text : '';
  }
  if (typeof value !== 'object') return '';
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) continue;
    const candidate = value[key];
    if (typeof candidate === 'string' && candidate.trim()) {
      const text = candidate.trim();
      if ((text.startsWith('{') && text.endsWith('}')) || (text.startsWith('[') && text.endsWith(']'))) {
        try {
          const nested = firstText(JSON.parse(text), keys, depth + 1);
          if (nested) return nested;
        } catch {}
      }
      return text;
    }
    if (candidate && typeof candidate === 'object') {
      const nested = firstText(candidate, keys, depth + 1);
      if (nested) return nested;
    }
  }
  for (const child of Object.values(value)) {
    if (!child || typeof child !== 'object') continue;
    const candidate = firstText(child, keys, depth + 1);
    if (candidate) return candidate;
  }
  return '';
}

// Douyin built-in sticker / light-interaction labels often arrive as plain text
// (e.g. content.text = "早点睡"). Without brackets they look like real chat text.
const DOUYIN_STICKER_LABELS = new Set([
  '早点睡', '续火花', '打招呼', '微笑', '赞', '比心', '爱心', '抱抱', '加油',
  '晚安', '早上好', '开心', '大哭', '生气', '疑问', '鼓掌', '捂脸', '色',
  '憨笑', '偷笑', '调皮', '呲牙', '惊讶', '可怜', '敲打', '再见', '擦汗',
  '抠鼻', '鼓掌', '坏笑', '左哼哼', '右哼哼', '哈欠', '鄙视', '委屈', '快哭了',
  '阴险', '亲亲', '吓', '可怜', '菜刀', '西瓜', '啤酒', '篮球', '乒乓',
  '咖啡', '饭', '猪头', '玫瑰', '凋谢', '示爱', '爱心', '心碎', '蛋糕',
  '闪电', '炸弹', '刀', '足球', '瓢虫', '便便', '月亮', '太阳', '礼物',
  '拥抱', '强', '弱', '握手', '胜利', '抱拳', '勾引', '拳头', '差劲',
  '爱你', 'NO', 'OK', '爱情', '飞吻', '跳跳', '发抖', '怄火', '转圈',
  '磕头', '回头', '跳绳', '挥手', '激动', '街舞', '献吻', '左太极', '右太极',
]);

function formatDouyinStickerText(text, messageType) {
  const label = String(text || '').trim();
  if (!label) return '';
  if (label === 'favorite_emoji') return '[表情]';
  if (label.startsWith('[') && label.endsWith(']')) return label;
  // Type 5 is emoji/sticker; named stickers also ride as type 7 plain text.
  if (messageType === 5 || DOUYIN_STICKER_LABELS.has(label)) {
    return `[${label}]`;
  }
  return label;
}

function replyDetails(content, ext = {}) {
  if ((!content || typeof content !== 'object') && (!ext || typeof ext !== 'object')) {
    return { quotedName: '', quotedText: '', quotedSenderUid: '', quotedServerMessageId: '', quotedClientMessageId: '' };
  }
  const empty = { quotedName: '', quotedText: '', quotedSenderUid: '', quotedServerMessageId: '', quotedClientMessageId: '' };
  const nameKeys = [
    'nickname', 'nick_name', 'nickName', 'user_nickname', 'userNickname',
    'sender_name', 'senderName', 'replyName', 'from_user_name', 'fromUserName',
    'user_name', 'userName', 'name', 'display_name', 'displayName', 'remark_name', 'remarkName',
  ];
  const textKeys = [
    'text', 'content', 'message', 'msg', 'replyText', 'quote_text', 'quotedText', 'hint',
    'msg_content', 'msgContent', 'body', 'summary', 'desc', 'content_text', 'contentText',
  ];
  const uidKeys = [
    'sender_uid', 'senderUid', 'from_uid', 'fromUid', 'user_id', 'userId', 'uid',
    'sec_sender', 'from_user_id', 'fromUserId',
  ];
  const serverIdKeys = [
    'server_message_id', 'serverMessageId', 'server_msg_id', 'serverMsgId',
    'ref_server_message_id', 'referenced_message_id', 'message_id', 'messageId', 'msg_id', 'msgId',
  ];
  const clientIdKeys = [
    'client_message_id', 'clientMessageId', 'client_msg_id', 'clientMsgId',
    'ref_client_message_id', 'target_client_message_id',
  ];

  const dig = (node) => {
    if (!node || typeof node !== 'object') return empty;
    const quotedName = firstText(node, nameKeys);
    const quotedText = firstText(node, textKeys);
    const quotedSenderUid = firstText(node, uidKeys);
    const quotedServerMessageId = firstText(node, serverIdKeys);
    const quotedClientMessageId = firstText(node, clientIdKeys);
    if (quotedName || quotedText || quotedSenderUid || quotedServerMessageId || quotedClientMessageId) {
      return { quotedName, quotedText, quotedSenderUid, quotedServerMessageId, quotedClientMessageId };
    }
    return empty;
  };

  const queue = [];
  if (content && typeof content === 'object') queue.push(content);
  if (ext && typeof ext === 'object') queue.push(ext);

  // Prefer keys that explicitly look like reply/quote/reference.
  while (queue.length > 0) {
    const current = queue.shift();
    if (!current || typeof current !== 'object' || Array.isArray(current)) {
      if (Array.isArray(current)) {
        for (const item of current) {
          if (item && typeof item === 'object') queue.push(item);
        }
      }
      continue;
    }
    for (const [key, value] of Object.entries(current)) {
      if (!value) continue;
      if (/reply|quote|reference|referenced|ref_msg|refMsg|ref_info|origin_msg|source_msg|parent_msg/i.test(key)) {
        let parsed = value;
        if (typeof parsed === 'string') {
          const maybe = parseMaybeJSON(parsed);
          if (maybe) parsed = maybe;
        }
        const hit = dig(parsed);
        if (hit.quotedName || hit.quotedText || hit.quotedSenderUid || hit.quotedServerMessageId || hit.quotedClientMessageId) {
          return hit;
        }
        if (typeof value === 'string' && value.trim() && !value.trim().startsWith('{')) {
          // Bare reply text under a reply_* key.
          return { ...empty, quotedText: value.trim() };
        }
      }
      if (value && typeof value === 'object') queue.push(value);
      else if (typeof value === 'string') {
        const maybe = parseMaybeJSON(value);
        if (maybe) queue.push(maybe);
      }
    }
  }

  // Secondary: dig top-level content/ext even if key names are opaque.
  const topHit = dig(content) || dig(ext);
  if (topHit && (topHit.quotedText || topHit.quotedServerMessageId)) {
    // Only accept if it looks like a nested reply object, not the current message body.
    // Avoid treating content.text as quotedText: dig() would pick content.text first.
  }

  // Ext-only ID refs (common when body is not duplicated).
  const extServer = firstText(ext, [
    's:ref_server_message_id', 's:referenced_message_id', 's:reply_server_message_id',
    'a:ref_server_message_id', 'a:referenced_message_id', 'ref_server_message_id',
    's:target_server_message_id',
  ]);
  const extClient = firstText(ext, [
    's:ref_client_message_id', 's:reply_client_message_id', 'a:ref_client_message_id',
    's:target_client_message_id', 'ref_client_message_id',
  ]);
  const extUid = firstText(ext, [
    's:ref_uid', 's:reply_uid', 'a:ref_uid', 's:ref_sender_uid', 'a:ref_sender_uid',
  ]);
  if (extServer || extClient || extUid) {
    return {
      quotedName: '',
      quotedText: firstText(ext, ['s:ref_text', 'a:ref_text', 's:reply_text', 'a:reply_text', 'ref_text']) || '',
      quotedSenderUid: extUid,
      quotedServerMessageId: extServer,
      quotedClientMessageId: extClient,
    };
  }
  return empty;
}

// Video / aweme share cards. Type 8 is the classic share; type 77 is the richer
// card used when forwarding a feed video (has itemId + cover + content_name author).
function isDouyinVideoShare(messageType, content) {
  if (!content || typeof content !== 'object') return false;
  if (messageType === 8 || messageType === 77) return true;
  const itemId = content.itemId || content.item_id || content.aweme_id || content.awemeId;
  if (!itemId) return false;
  return Boolean(
    content.cover_url
    || content.content_thumb
    || content.awemeType != null
    || content.aweType != null
    || content.content_title
    || content.play_addr
  );
}

function formatDouyinVideoShare(content) {
  const itemId = String(content?.itemId || content?.item_id || content?.aweme_id || content?.awemeId || '').trim();
  // Prefer video title, never author nickname (content_name) as the body text.
  const title = String(content?.content_title || content?.title || content?.desc || '').trim();
  const text = title ? `[视频] ${title}` : '[视频]';
  const link = itemId ? `https://www.douyin.com/video/${itemId}` : '';
  return { text, link };
}

function parseMaybeJSON(value) {
  if (value == null) return null;
  if (typeof value === 'object') return value;
  if (typeof value !== 'string') return null;
  const text = value.trim();
  if (!text) return null;
  if (!(text.startsWith('{') || text.startsWith('['))) return null;
  try { return JSON.parse(text); } catch { return null; }
}

// Type 50002 / 70002 are Douyin "light interaction" stickers (续火花 / 打招呼 / 早点睡…).
// Payload often lives in ext["a:light_interaction"] rather than content.text.
function extractLightInteractionLabel(content, ext = {}) {
  const bags = [
    content,
    parseMaybeJSON(ext['a:light_interaction']),
    parseMaybeJSON(ext['a:light_interaction_mob']),
    parseMaybeJSON(ext.light_interaction),
    parseMaybeJSON(ext.light_interaction_mob),
    ext,
  ].filter(Boolean);

  const keys = [
    'text', 'content', 'name', 'title', 'label', 'emoji_name', 'emojiName',
    'interaction_name', 'interactionName', 'sticker_name', 'stickerName',
    'display_name', 'displayName', 'hint', 'msg',
  ];
  for (const bag of bags) {
    const label = firstText(bag, keys);
    if (!label) continue;
    if (label === 'favorite_emoji') return '[表情]';
    if (label.startsWith('[') && label.endsWith(']')) return label;
    // Prefer short sticker-like labels; still accept longer ones when type is light interaction.
    return formatDouyinStickerText(label, 5) || `[${label}]`;
  }
  return '';
}

function isLightInteractionMessage(messageType, content, ext = {}) {
  if (messageType === 50002 || messageType === 70002) return true;
  return Boolean(
    ext['a:light_interaction']
    || ext['a:light_interaction_mob']
    || ext.light_interaction
    || content?.light_interaction
  );
}

// Type 1 is Douyin IM system / in-chat notice (not user chat).
// Real sample 2026-07-18 陆苇: keys aweType,scene,tips,template,... scene=go_to_maya_notice
// firstText used to walk scene as body text → "go_to_maya_notice" leaked to QQ.
function isDouyinSystemNotice(messageType, content = {}) {
  if (messageType === 1) return true;
  if (!content || typeof content !== 'object') return false;
  if (content.scene === 'go_to_maya_notice') return true;
  if (content.template != null && content.tips != null && content.show_on_screen != null) return true;
  return false;
}

// Type 50001 (+ command_type) is server control / conv metadata (achieve_task, pet closeness…).
// 2026-07-19 sample: leaked to QQ as 「[暂不支持的消息]」 alongside a real type=7 frame.
function isDouyinControlCommand(messageType, content = {}, ext = {}) {
  if (messageType === 50001) return true;
  if (content && typeof content === 'object' && content.command_type != null) return true;
  const biz = String(ext['a:biz'] || ext.biz || content?.a_biz || '').trim();
  if (biz === 'conv_biz_ext_change_cmd') return true;
  return false;
}

// Compact share / card previews for quote lines: keep 「[分享图文]」 not the whole essay.
function compactDouyinShareQuote(text) {
  const raw = String(text || '').trim();
  if (!raw) return '';
  if (raw.startsWith('[分享图文]')) return '[分享图文]';
  if (raw.startsWith('[分享]')) return '[分享]';
  if (raw.startsWith('[视频]')) {
    const rest = raw.slice('[视频]'.length).trim();
    return rest ? `[视频] ${rest.slice(0, 40)}${rest.length > 40 ? '…' : ''}` : '[视频]';
  }
  if (raw.length > 80) return `${raw.slice(0, 80)}…`;
  return raw;
}

function looksLikeDouyinShareCardText(text) {
  const raw = String(text || '').trim();
  return Boolean(
    raw.startsWith('[分享图文]')
    || raw.startsWith('[分享]')
    || raw.startsWith('[视频]')
  );
}

function formatDouyinSystemNotice(content = {}) {
  // Prefer human-facing tips/template text when present; else a stable label.
  const tips = firstText(content, ['tips', 'tip', 'template', 'title', 'description', 'text', 'content', 'message']);
  if (tips && !/^[a-z][a-z0-9_]*$/i.test(tips)) return `[系统提示] ${tips}`;
  const scene = String(content?.scene || '').trim();
  if (scene) return `[系统提示] ${scene}`;
  return '[系统提示]';
}

function contentPreview(raw, max = 180) {
  const text = String(raw || '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.length <= max ? text : `${text.slice(0, max)}…`;
}

// When field 8 is not JSON (common suspicion for image-reply frames), try to pull
// readable UTF-8 runs out of the raw bytes so we can at least see 哈哈 / 这是要干嘛.
function extractPrintableRuns(bytes, minLen = 2, maxRuns = 12) {
  if (!(bytes instanceof Uint8Array) || bytes.length === 0) return [];
  const runs = [];
  let buf = [];
  const flush = () => {
    if (buf.length < minLen) {
      buf = [];
      return;
    }
    try {
      const text = textDecoder.decode(Uint8Array.from(buf)).replace(/\u0000/g, '').trim();
      if (text && /[\u4e00-\u9fffA-Za-z0-9]/.test(text)) runs.push(text);
    } catch {}
    buf = [];
  };
  for (const byte of bytes) {
    // keep printable ASCII + UTF-8 continuation / lead bytes loosely
    if (byte === 0x09 || byte === 0x0a || byte === 0x0d || (byte >= 0x20 && byte !== 0x7f)) {
      buf.push(byte);
    } else {
      flush();
    }
    if (runs.length >= maxRuns) break;
  }
  flush();
  return runs.slice(0, maxRuns);
}

function hexPreview(bytes, max = 48) {
  if (!(bytes instanceof Uint8Array) || bytes.length === 0) return '';
  const slice = bytes.slice(0, max);
  return [...slice].map((b) => b.toString(16).padStart(2, '0')).join('');
}

function summarizeExt(ext = {}, maxEntries = 12, maxValue = 120) {
  const out = {};
  let n = 0;
  for (const [key, value] of Object.entries(ext || {})) {
    if (n >= maxEntries) break;
    const text = String(value ?? '').replace(/\s+/g, ' ').trim();
    // Skip pure noise timestamps / long tokens unless short.
    if (!text) continue;
    if (/^(true|false|\d{10,})$/i.test(text) && !/reply|quote|text|msg|content|image|ref/i.test(key)) continue;
    out[key] = text.length <= maxValue ? text : `${text.slice(0, maxValue)}…`;
    n += 1;
  }
  return out;
}

// Prefer human chat body from content/ext; never pick machine scene ids.
function extractChatBody(content = {}, ext = {}) {
  const preferredKeys = [
    'text', 'content', 'message', 'msg', 'title', 'description', 'content_title',
    'hint', 'body', 'richText', 'rich_text', 'display_text', 'displayText',
  ];
  const contentWithoutReply = Object.fromEntries(
    Object.entries(content || {}).filter(([key]) => !/reply|quote|reference|referenced/i.test(key)),
  );
  let text = firstText(contentWithoutReply, preferredKeys);
  if (text && (content?.content_name === text || content?.author_name === text || content?.nickname === text)) {
    if (!content?.text && !content?.content_title && !content?.title && !content?.description) {
      text = '';
    }
  }
  if (text) return text;

  // Some reply-to-media frames put body only in ext JSON blobs.
  for (const [key, value] of Object.entries(ext || {})) {
    if (!/reply|quote|ref|content|text|msg|body|hint|display/i.test(key)) continue;
    const parsed = parseMaybeJSON(value);
    if (parsed) {
      const nested = firstText(parsed, preferredKeys);
      if (nested && !/^[a-z][a-z0-9_]*$/i.test(nested)) return nested;
    }
    const raw = String(value || '').trim();
    if (raw && /[\u4e00-\u9fff]/.test(raw) && raw.length <= 200 && !raw.startsWith('{')) return raw;
  }
  return '';
}

// Field 8 is usually JSON, but image-reply frames may use nested protobuf strings.
function extractTextFromNestedProtobuf(bytes) {
  if (!(bytes instanceof Uint8Array) || bytes.length < 2) {
    return { text: '', quotedName: '', quotedText: '', quotedSenderUid: '', quotedServerMessageId: '', quotedClientMessageId: '' };
  }
  try {
    const fields = decodeFields(bytes);
    const preferredKeys = [
      'text', 'content', 'message', 'msg', 'title', 'description', 'content_title',
      'hint', 'body', 'richText', 'rich_text', 'display_text', 'displayText',
    ];
    const bag = {};
    for (const values of fields.values()) {
      for (const value of values || []) {
        if (!(value instanceof Uint8Array)) continue;
        const asStr = asString(value);
        if (!asStr) continue;
        try {
          const parsed = JSON.parse(asStr);
          if (parsed && typeof parsed === 'object') Object.assign(bag, parsed);
        } catch {
          if (/[\u4e00-\u9fff]/.test(asStr) && asStr.length <= 200) {
            bag.text = bag.text || asStr;
          }
        }
      }
    }
    const reply = replyDetails(bag, {});
    const text = firstText(bag, preferredKeys) || '';
    return {
      text: text && !/^[a-z][a-z0-9_]*$/i.test(text) ? text : '',
      quotedName: reply.quotedName,
      quotedText: reply.quotedText,
      quotedSenderUid: reply.quotedSenderUid,
      quotedServerMessageId: reply.quotedServerMessageId || '',
      quotedClientMessageId: reply.quotedClientMessageId || '',
      bagKeys: Object.keys(bag),
    };
  } catch {
    return { text: '', quotedName: '', quotedText: '', quotedSenderUid: '', quotedServerMessageId: '', quotedClientMessageId: '' };
  }
}

// Scan remaining protobuf fields for JSON / Chinese chat text when field 8 is empty
// or non-JSON. Image-reply frames may stash body outside classic content JSON.
function extractTextFromOtherFields(message, skipFields = new Set([1, 2, 3, 4, 5, 6, 7, 9, 10, 14])) {
  const preferredKeys = [
    'text', 'content', 'message', 'msg', 'title', 'description', 'content_title',
    'hint', 'body', 'richText', 'rich_text', 'display_text', 'displayText',
  ];
  const candidates = [];
  for (const [number, values] of message.entries()) {
    if (skipFields.has(number)) continue;
    for (const value of values || []) {
      if (!(value instanceof Uint8Array) || value.length === 0) continue;
      const asStr = asString(value);
      if (!asStr) continue;
      try {
        const parsed = JSON.parse(asStr);
        if (parsed && typeof parsed === 'object') {
          const nested = firstText(parsed, preferredKeys);
          if (nested && !/^[a-z][a-z0-9_]*$/i.test(nested)) {
            candidates.push({ number, text: nested, source: 'json' });
          }
          continue;
        }
      } catch {}
      // Nested protobuf inside this field.
      const nestedPb = extractTextFromNestedProtobuf(value);
      if (nestedPb.text) {
        candidates.push({ number, text: nestedPb.text, source: 'nested-pb' });
        continue;
      }
      const runs = extractPrintableRuns(value, 2, 8)
        .filter((run) => /[\u4e00-\u9fff]/.test(run) && run.length <= 200 && !/^[a-z][a-z0-9_]*$/i.test(run));
      for (const run of runs) candidates.push({ number, text: run, source: 'raw' });
    }
  }
  if (candidates.length === 0) return { text: '', fieldHits: [] };
  candidates.sort((a, b) => b.text.length - a.text.length);
  return {
    text: candidates[0].text,
    fieldHits: candidates.slice(0, 6).map((c) => `${c.number}:${c.source}:${c.text.slice(0, 40)}`),
  };
}

function decodeMessage(raw, fallbackConversationId = '', fallbackConversationType = 0) {
  const message = decodeFields(raw);
  const contentField = first(message, 8);
  const contentBytes = contentField instanceof Uint8Array ? contentField : null;
  const contentRaw = asString(contentField);
  let content = {};
  let contentParseOk = false;
  if (contentRaw) {
    try {
      const parsed = JSON.parse(contentRaw);
      if (parsed && typeof parsed === 'object') {
        content = parsed;
        contentParseOk = true;
      }
    } catch {
      contentParseOk = false;
    }
  } else {
    contentParseOk = true; // empty body is a valid empty content
  }
  const ext = mapEntries(message, 9);
  const messageType = Number(first(message, 6, 0n));
  let text = typeof content?.text === 'string' ? content.text.trim() : '';
  const replyFromContent = replyDetails(content, ext);
  let quotedName = replyFromContent.quotedName;
  let quotedText = replyFromContent.quotedText;
  let quotedSenderUid = replyFromContent.quotedSenderUid;
  let quotedServerMessageId = replyFromContent.quotedServerMessageId || '';
  let quotedClientMessageId = replyFromContent.quotedClientMessageId || '';
  let link = '';
  const fieldNumbers = [...message.keys()].sort((a, b) => a - b);
  let fieldHits = [];

  // Control / conv-biz commands never carry chat body.
  if (isDouyinControlCommand(messageType, content, ext)) {
    text = '[控制消息]';
  } else if (isDouyinVideoShare(messageType, content)) {
    // Video shares must be classified before firstText walks content_name (author).
    const video = formatDouyinVideoShare(content);
    text = video.text;
    link = video.link;
  } else if (isLightInteractionMessage(messageType, content, ext)) {
    text = extractLightInteractionLabel(content, ext) || '[表情]';
  } else if (isDouyinSystemNotice(messageType, content)) {
    // Keep a short label for logs; mark as internal so QQ does not forward machine scenes.
    text = formatDouyinSystemNotice(content);
  } else {
    if (!text) {
      text = extractChatBody(content, ext);
    }
    // Non-JSON field 8: try nested protobuf, then printable Chinese runs.
    if (!text && !contentParseOk && contentBytes) {
      const nested = extractTextFromNestedProtobuf(contentBytes);
      if (nested.text) {
        text = nested.text;
        fieldHits = [`8:nested-pb:${nested.text.slice(0, 40)}`];
        if (!quotedName && nested.quotedName) quotedName = nested.quotedName;
        if (!quotedText && nested.quotedText) quotedText = nested.quotedText;
        if (!quotedSenderUid && nested.quotedSenderUid) quotedSenderUid = nested.quotedSenderUid;
        if (!quotedServerMessageId && nested.quotedServerMessageId) quotedServerMessageId = nested.quotedServerMessageId;
        if (!quotedClientMessageId && nested.quotedClientMessageId) quotedClientMessageId = nested.quotedClientMessageId;
      } else {
        const runs = extractPrintableRuns(contentBytes, 2, 16)
          .filter((run) => /[\u4e00-\u9fff]/.test(run) && !/^[a-z][a-z0-9_]*$/i.test(run));
        if (runs.length === 1) text = runs[0];
        else if (runs.length > 1) {
          text = runs.sort((a, b) => b.length - a.length)[0];
        }
      }
    }
    // Empty / useless field 8: scan other protobuf fields (image-reply hypothesis).
    // Real 2026-07-19 sample: type=7 empty content, field 18 nested-pb carries the
    // quoted 「[分享图文]…」 that the peer is replying to — NOT the peer's body.
    if (!text) {
      const other = extractTextFromOtherFields(message);
      if (other.text) {
        fieldHits = other.fieldHits;
        if (!quotedText && looksLikeDouyinShareCardText(other.text)) {
          quotedText = compactDouyinShareQuote(other.text);
          // Leave body empty so we don't attribute the share card to the peer.
        } else if (!quotedText && (quotedServerMessageId || quotedClientMessageId || quotedName)) {
          // Have explicit reply refs but no body: treat scavenged text as quote if long card-like.
          quotedText = compactDouyinShareQuote(other.text);
        } else {
          text = other.text;
        }
      }
    } else if (!quotedText && looksLikeDouyinShareCardText(text) && !contentParseOk && messageType === 7) {
      // Body-only path also picked share card from nested pb with empty JSON content.
      // Prefer quote stack: 「我/对方：[分享图文]」 + real reply (or [回复]).
      quotedText = compactDouyinShareQuote(text);
      text = '';
    }
    text = formatDouyinStickerText(text, messageType);
  }

  // If we still have no body but replyDetails found a quote, surface a reply placeholder
  // so QQ at least shows "replied to X" instead of 暂不支持 — only when quote exists.
  if (!text && (quotedText || quotedName || quotedServerMessageId || quotedClientMessageId)) {
    text = '[回复]';
  }

  // System notices / control metadata should not be forwarded to QQ.
  const internalMetadata = /^\d+:\d+:\d+:\d+$/.test(text)
    || isDouyinSystemNotice(messageType, content)
    || isDouyinControlCommand(messageType, content, ext)
    || text === 'go_to_maya_notice'
    || text === '[控制消息]';
  if (!text) {
    text = ({ 5: '[表情]', 17: '[语音]', 27: '[图片]', 50002: '[表情]', 70002: '[表情]' })[messageType] || '[暂不支持的消息]';
  }
  const contentKeyList = Object.keys(content || {});
  const rawRuns = (!contentParseOk && contentBytes)
    ? extractPrintableRuns(contentBytes, 2, 8)
    : [];
  return {
    conversationId: asString(first(message, 1)) || fallbackConversationId,
    conversationType: Number(first(message, 2, BigInt(fallbackConversationType))),
    serverMessageId: asString(first(message, 3)),
    index: asString(first(message, 4)),
    conversationShortId: asString(first(message, 5)),
    messageType,
    senderUid: asString(first(message, 7)),
    senderSecUid: asString(first(message, 14)),
    senderNameHint: ext['s:sender_nickname'] || ext.sender_nickname || ext['a:sender_nickname'] || ext['s:sender_name'] || '',
    createTime: asNumber(first(message, 10, 0n)),
    clientMessageId: ext['s:client_message_id'] || ext.client_message_id || ext['a:client_message_id'] || '',
    quotedName,
    quotedText,
    quotedSenderUid,
    quotedServerMessageId,
    quotedClientMessageId,
    internalMetadata,
    contentKeys: [...contentKeyList, ...Object.keys(ext).map((key) => `ext:${key}`)].slice(0, 20),
    contentParseOk,
    contentLen: contentBytes ? contentBytes.length : (contentRaw ? contentRaw.length : 0),
    contentPreview: contentParseOk
      ? contentPreview(contentRaw)
      : contentPreview(rawRuns.join(' | ') || contentRaw),
    contentHex: contentParseOk ? '' : hexPreview(contentBytes || new Uint8Array(), 64),
    fieldNumbers,
    fieldHits,
    extSummary: summarizeExt(ext),
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

export function isOwnDouyinIMMessage(senderUid, selfUid) {
  const sender = String(senderUid || '').trim();
  const self = String(selfUid || '').trim();
  return sender !== '' && self !== '' && sender === self;
}

export {
  compactDouyinShareQuote,
  isDouyinControlCommand,
  isDouyinSystemNotice,
  looksLikeDouyinShareCardText,
};

export const douyinIMInternals = { decodeFields, asString, asNumber };
