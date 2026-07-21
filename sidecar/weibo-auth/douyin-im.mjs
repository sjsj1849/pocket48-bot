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

// Video / aweme share cards.
// - type 8: classic share
// - type 77: richer feed card (itemId + cover + content_name author)
// - type 105: comment/share-with-video card (aweme_title + cover + often comment_*)
function isDouyinVideoShare(messageType, content) {
  if (!content || typeof content !== 'object') return false;
  if (messageType === 8 || messageType === 77 || messageType === 105) return true;
  const itemId = content.itemId || content.item_id || content.aweme_id || content.awemeId;
  if (!itemId) return false;
  return Boolean(
    content.cover_url
    || content.content_thumb
    || content.awemeType != null
    || content.aweType != null
    || content.content_title
    || content.aweme_title
    || content.play_addr
    || content.media_type === 4
    || content.media_type === '4'
  );
}

function firstCoverURL(content = {}) {
  const candidates = [];
  collectURLCandidates(content.cover_url, candidates);
  collectURLCandidates(content.content_thumb, candidates);
  collectURLCandidates(content.cover, candidates);
  collectURLCandidates(content.thumb, candidates);
  // Nested objects sometimes hold url_list.
  collectURLCandidates(content.video, candidates);
  collectURLCandidates(content.cover_image, candidates);
  collectURLCandidates(content.image, candidates);
  collectURLCandidates(content.image_url, candidates);
  collectURLCandidates(content.poster, candidates);
  collectURLCandidates(content?.related_share_video, candidates);
  collectURLCandidates(content?.aweme, candidates);
  collectURLCandidates(content?.aweme_info, candidates);
  // Prefer image-looking CDN URLs; one is enough for a card cover.
  const cleaned = [];
  for (const url of candidates) {
    if (!/^https?:\/\//i.test(url)) continue;
    if (url.startsWith('data:')) continue;
    if (!cleaned.includes(url)) cleaned.push(url);
  }
  if (cleaned.length === 0) return '';
  // Prefer jpeg/webp/png static covers over weird tokens.
  cleaned.sort((a, b) => {
    const score = (u) => {
      let s = 0;
      const x = u.toLowerCase();
      if (/\.(jpg|jpeg|png|webp)(\?|$)/i.test(x)) s += 3;
      if (/cover|thumb|origin|large/i.test(x)) s += 2;
      if (/avatar|profile/i.test(x)) s -= 2;
      return s;
    };
    return score(b) - score(a);
  });
  return cleaned[0];
}

// Dig itemId / aweme_id from nested bags (type 8 private shares often nest fields).
function extractDouyinItemId(content = {}, ext = {}) {
  const keys = [
    'itemId', 'item_id', 'aweme_id', 'awemeId', 'awemeid',
    'group_id', 'groupId', 'video_id', 'videoId', 'media_id', 'mediaId',
  ];
  const bags = [
    content,
    content?.related_share_video,
    content?.aweme,
    content?.aweme_info,
    content?.video,
    content?.share_info,
    content?.shareInfo,
    content?.card,
    content?.card_content,
    content?.raw_data,
    content?.rawData,
    parseMaybeJSON(content?.content),
    parseMaybeJSON(content?.extra),
    parseMaybeJSON(content?.data),
  ];
  for (const [k, v] of Object.entries(ext || {})) {
    if (!/aweme|item|video|share|card|media|content/i.test(k)) continue;
    bags.push(parseMaybeJSON(v) || (typeof v === 'object' ? v : null));
  }
  const visit = (node, depth = 0) => {
    if (!node || depth > 5) return '';
    if (typeof node === 'string') {
      const s = node.trim();
      // bare numeric id
      if (/^\d{8,}$/.test(s)) return s;
      // URL …/video/123456
      const m = s.match(/\/video\/(\d{8,})/i) || s.match(/[?&](?:modal_id|aweme_id|item_id)=(\d{8,})/i);
      if (m) return m[1];
      return '';
    }
    if (typeof node !== 'object') return '';
    for (const key of keys) {
      if (node[key] != null && String(node[key]).trim()) {
        const id = String(node[key]).trim();
        if (/^\d{6,}$/.test(id) || id.length >= 8) return id;
      }
    }
    for (const child of Object.values(node)) {
      const id = visit(child, depth + 1);
      if (id) return id;
    }
    return '';
  };
  for (const bag of bags) {
    const id = visit(bag, 0);
    if (id) return id;
  }
  // Last resort: scan stringified content for video URLs / long digit ids near aweme keywords.
  try {
    const blob = JSON.stringify(content || {}).slice(0, 8000);
    const m = blob.match(/\/video\/(\d{8,})/i)
      || blob.match(/"(?:itemId|item_id|aweme_id|awemeId)"\s*:\s*"?(\d{8,})/i);
    if (m) return m[1];
  } catch {}
  return '';
}

function formatDouyinVideoShare(content = {}, ext = {}) {
  const itemId = extractDouyinItemId(content, ext)
    || String(
      content.itemId
      || content.item_id
      || content.aweme_id
      || content.awemeId
      || content?.related_share_video?.itemId
      || '',
    ).trim();

  // Prefer real video title fields; never use author nickname (content_name) as body.
  let title = String(
    content.content_title
    || content.aweme_title
    || content.title
    || content.desc
    || content.msgHint
    || content.content_desc
    || content.share_title
    || content.shareTitle
    || content?.related_share_video?.title
    || content?.related_share_video?.desc
    || '',
  ).trim();
  // Strip trailing hashtag spam a bit for QQ readability.
  if (title.length > 80) {
    title = `${title.slice(0, 80)}…`;
  }

  const author = String(
    content.content_name
    || content.aweme_author_name
    || content.author_name
    || content.authorName
    || content.nickname
    || content?.related_share_video?.author_name
    || '',
  ).trim();

  // type 105 often shares a video *with a comment* on it.
  const comment = String(
    content.comment
    || content.comment_text
    || content.commentText
    || '',
  ).trim();
  const commentUser = String(
    content.comment_user_name
    || content.commentUserName
    || '',
  ).trim();

  // Prefer explicit share URL if present.
  let link = String(
    content.share_url
    || content.shareUrl
    || content.video_url
    || content.videoUrl
    || content.schema
    || content.schema_url
    || content.link
    || content.url
    || '',
  ).trim();
  if (link && !/^https?:\/\//i.test(link)) {
    // aweme schema etc — drop, rebuild from itemId when possible
    if (!/douyin\.com/i.test(link)) link = '';
  }
  if (!link && itemId) link = `https://www.douyin.com/video/${itemId}`;
  // If link carries id but itemId empty, keep link.

  const lines = [];
  if (title) lines.push(`[视频] ${title}`);
  else lines.push('[视频]');
  if (author) lines.push(`作者：${author}`);
  if (comment) {
    lines.push(commentUser ? `评论 ${commentUser}：${comment}` : `评论：${comment}`);
  }
  // When we have no title/cover/item, still surface link if we found one elsewhere.
  if (lines.length === 1 && lines[0] === '[视频]' && !link) {
    // leave as placeholder; caller may attach images/link from other recovery
  }

  const cover = firstCoverURL(content) || firstCoverURL(content?.related_share_video || {});
  return {
    text: lines.join('\n'),
    link,
    cover,
    itemId,
  };
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
// Some frames also carry sticker image URL(s) we can forward to QQ as real images.
function lightInteractionBags(content, ext = {}) {
  return [
    content,
    parseMaybeJSON(ext['a:light_interaction']),
    parseMaybeJSON(ext['a:light_interaction_mob']),
    parseMaybeJSON(ext.light_interaction),
    parseMaybeJSON(ext.light_interaction_mob),
    parseMaybeJSON(ext['a:sticker']),
    parseMaybeJSON(ext.sticker),
    parseMaybeJSON(content?.sticker),
    parseMaybeJSON(content?.emoji),
    parseMaybeJSON(content?.emoticon),
    ext,
  ].filter(Boolean);
}

function extractLightInteractionLabel(content, ext = {}) {
  const bags = lightInteractionBags(content, ext);
  const keys = [
    'text', 'content', 'name', 'title', 'label', 'emoji_name', 'emojiName',
    'interaction_name', 'interactionName', 'sticker_name', 'stickerName',
    'display_name', 'displayName', 'hint', 'msg', 'desc', 'emoji_desc',
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

function extractStickerImageURLs(content = {}, ext = {}) {
  const out = [];
  for (const bag of lightInteractionBags(content, ext)) {
    collectURLCandidates(bag, out);
  }
  // Also walk raw content for classic sticker shapes (url_list / static_url / animate_url).
  collectURLCandidates(content, out);
  const cleaned = [];
  for (const url of out) {
    if (!/^https?:\/\//i.test(url)) continue;
    if (url.startsWith('data:')) continue;
    if (!cleaned.includes(url)) cleaned.push(url);
  }
  // Stickers often expose static_url + animate_url (same asset). Prefer one:
  // static/png first, then any remaining unique by path basename without query.
  return pickBestStickerURLs(cleaned, 1);
}

// Collapse same-sticker multi-URL payloads (static + animate) to a single best URL.
function pickBestStickerURLs(urls, max = 1) {
  if (!Array.isArray(urls) || urls.length === 0) return [];
  const score = (url) => {
    const u = String(url).toLowerCase();
    let s = 0;
    if (/\.(png|webp|jpg|jpeg)(\?|$)/i.test(u)) s += 3;
    if (/static|thumb|origin|large/i.test(u)) s += 2;
    if (/animate|gif|awebp|heic/i.test(u)) s -= 1;
    return s;
  };
  const byBase = new Map();
  for (const url of urls) {
    let base = url;
    try {
      const parsed = new URL(url);
      base = parsed.pathname.replace(/\/+$/, '') || url;
    } catch {
      base = url.split('?')[0];
    }
    const prev = byBase.get(base);
    if (!prev || score(url) > score(prev)) byBase.set(base, url);
  }
  return [...byBase.values()]
    .sort((a, b) => score(b) - score(a))
    .slice(0, Math.max(1, max));
}

// Placeholder captions that are redundant once a real sticker/image is attached.
function isStickerCaptionText(text) {
  const t = String(text || '').trim();
  if (!t) return false;
  if (t === '[表情]' || t === '[贴纸]') return true;
  // Do NOT treat [图片]/[视频] as sticker captions — those are media type labels.
  // [早点睡] / [比心] / [续火花] — short bracket light-interaction labels.
  if (/^\[[^\]\n]{1,12}\]$/.test(t) && t !== '[图片]' && t !== '[视频]' && t !== '[语音]') return true;
  return false;
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

// Type 110: client-rendered special cards / video-emoji.
// - 2026-07-17: video_emoji_rec / use_default_emoji → no body text (show [表情])
// - 2026-07-19 20:56:58 张若昀 private: empty content, UI 「你还没有喂精灵～」, push has zero text/hex
//   → heuristic label so QQ is readable. If a future 110 carries real text, prefer that.
function formatDouyinType110(content = {}, ext = {}) {
  const fromContent =
    (typeof content?.text === 'string' && content.text.trim())
    || (typeof content?.title === 'string' && content.title.trim())
    || (typeof content?.msgHint === 'string' && content.msgHint.trim())
    || (typeof content?.hint === 'string' && content.hint.trim())
    || '';
  if (fromContent) return fromContent.startsWith('[') ? fromContent : `[${fromContent}]`;

  if (
    ext['a:video_emoji_rec']
    || ext['a:use_default_emoji']
    || ext.video_emoji_rec
    || ext.use_default_emoji
  ) {
    return '[表情]';
  }

  // Empty body: pet-feed / interactive reminder cards render only on client.
  const keys = content && typeof content === 'object' ? Object.keys(content) : [];
  if (keys.length === 0) {
    return '[你还没有喂精灵～]';
  }
  return '';
}

// Compact share / card previews for quote lines.
// Keep a short title when present: 「[分享图文] 缪尼火速二婚了」 not bare 「[分享图文]」.
function compactDouyinShareQuote(text) {
  const raw = String(text || '').trim();
  if (!raw) return '';
  if (raw.startsWith('[分享图文]')) {
    const rest = raw.slice('[分享图文]'.length).trim().replace(/^[:：\-\s]+/, '');
    if (rest) {
      const short = rest.length > 40 ? `${rest.slice(0, 40)}…` : rest;
      return `[分享图文] ${short}`;
    }
    return '[分享图文]';
  }
  if (raw.startsWith('[分享]')) {
    const rest = raw.slice('[分享]'.length).trim().replace(/^[:：\-\s]+/, '');
    if (rest) {
      const short = rest.length > 40 ? `${rest.slice(0, 40)}…` : rest;
      return `[分享] ${short}`;
    }
    return '[分享]';
  }
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
    || /^\[分享图文\]/.test(raw)
  );
}

// Build a compact share-card label from nested JSON (article/link/note/video cards).
// Used when a reply frame embeds the quoted card instead of plain text.
function formatDouyinShareCardFromBag(bag = {}) {
  if (!bag || typeof bag !== 'object') return '';
  // Already formatted labels
  const existing = firstText(bag, ['text', 'msgHint', 'hint', 'display_text', 'displayText']);
  if (looksLikeDouyinShareCardText(existing)) return compactDouyinShareQuote(existing);

  // Video / aweme nested in quote payload
  if (isDouyinVideoShare(0, bag) || bag.itemId || bag.item_id || bag.aweme_id || bag.awemeId || bag?.related_share_video) {
    const video = formatDouyinVideoShare(bag);
    // One-line quote form: 「[视频] 标题」 (drop 作者/评论 multi-line noise for quote stack)
    const firstLine = String(video.text || '').split('\n')[0].trim() || '[视频]';
    return compactDouyinShareQuote(firstLine);
  }

  const title = firstText(bag, [
    'title', 'content_title', 'contentTitle', 'aweme_title', 'desc', 'description',
    'summary', 'card_title', 'cardTitle', 'link_title', 'linkTitle', 'name',
  ]);
  const author = firstText(bag, [
    'content_name', 'author_name', 'authorName', 'nickname', 'user_name', 'userName',
  ]);
  const cover = firstCoverURL(bag);
  const hasCardShape = Boolean(
    title
    || cover
    || bag.card_type != null
    || bag.cardType != null
    || bag.link_url
    || bag.linkUrl
    || bag.share_url
    || bag.shareUrl
    || bag.article_url
    || bag.schema
  );
  if (!hasCardShape && !title) return '';

  const parts = ['[分享图文]'];
  if (title) parts.push(title.slice(0, 40) + (title.length > 40 ? '…' : ''));
  else if (author) parts.push(author);
  return parts.join(' ').trim();
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

// Type 27 image messages: content is JSON with url / url_list / cover etc, no text.
// Return absolute http(s) URLs so QQ can fetch them.
function collectURLCandidates(value, out, depth = 0) {
  if (depth > 5 || value == null || out.length >= 9) return;
  if (typeof value === 'string') {
    const s = value.trim();
    if (/^https?:\/\//i.test(s) && !out.includes(s)) {
      // Prefer image-looking URLs; still accept generic CDN links for type 27.
      out.push(s);
    }
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) collectURLCandidates(item, out, depth + 1);
    return;
  }
  if (typeof value !== 'object') return;
  // Prefer explicit image keys first.
  const preferred = [
    'url_list', 'urlList', 'origin_url_list', 'originUrlList',
    'download_url_list', 'downloadUrlList', 'big_url_list', 'bigUrlList',
    'url', 'image_url', 'imageUrl', 'resource_url', 'resourceUrl',
    'cover_url', 'coverUrl', 'content_thumb', 'contentThumb',
    'uri', 'src', 'large', 'origin', 'thumb', 'medium',
  ];
  for (const key of preferred) {
    if (Object.prototype.hasOwnProperty.call(value, key)) {
      collectURLCandidates(value[key], out, depth + 1);
    }
  }
  // Then walk remaining object values (skip huge binary-ish keys).
  for (const [key, child] of Object.entries(value)) {
    if (preferred.includes(key)) continue;
    if (/base64|encrypt|token|ticket|hex/i.test(key)) continue;
    collectURLCandidates(child, out, depth + 1);
  }
}

function extractImageURLs(content = {}, ext = {}, messageType = 0) {
  const out = [];
  collectURLCandidates(content, out);
  // Ext may carry image JSON blobs.
  for (const [key, value] of Object.entries(ext || {})) {
    if (!/image|pic|media|cover|thumb|resource|url/i.test(key)) continue;
    const parsed = parseMaybeJSON(value);
    collectURLCandidates(parsed || value, out);
  }
  // Prefer larger / original looking URLs: de-dup keep order, drop data: and non-http.
  const cleaned = [];
  for (const url of out) {
    if (!/^https?:\/\//i.test(url)) continue;
    if (url.startsWith('data:')) continue;
    if (!cleaned.includes(url)) cleaned.push(url);
  }
  // If type is image and we found nothing, still empty — caller keeps [图片].
  if (messageType === 27 && cleaned.length === 0) return [];
  return cleaned.slice(0, 9);
}

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
      'reply_text', 'replyText', 'comment', 'comment_text', 'commentText',
    ];
    const bag = {};
    const chineseRuns = [];
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
            chineseRuns.push(asStr.trim());
            bag.text = bag.text || asStr.trim();
          }
        }
      }
    }
    const reply = replyDetails(bag, {});
    // Prefer explicit body fields; also accept bare Chinese runs that are NOT share cards.
    let text = firstText(bag, preferredKeys) || '';
    if (text && looksLikeDouyinShareCardText(text)) {
      // body looks like quote card — keep as quote, clear body for now
      if (!reply.quotedText) reply.quotedText = compactDouyinShareQuote(text);
      text = '';
    }
    if (!text) {
      const nonCardRun = chineseRuns
        .filter((r) => r && !looksLikeDouyinShareCardText(r) && !/^[a-z][a-z0-9_]*$/i.test(r))
        .sort((a, b) => b.length - a.length)[0];
      if (nonCardRun) text = nonCardRun;
    }
    // Build share card quote from bag fields (title etc.) when nested only has card meta.
    let quotedText = reply.quotedText || '';
    if (!quotedText) {
      const card = formatDouyinShareCardFromBag(bag);
      if (card) quotedText = card;
    } else {
      quotedText = compactDouyinShareQuote(quotedText) || quotedText;
    }
    return {
      text: text && !/^[a-z][a-z0-9_]*$/i.test(text) ? text : '',
      quotedName: reply.quotedName,
      quotedText,
      quotedSenderUid: reply.quotedSenderUid,
      quotedServerMessageId: reply.quotedServerMessageId || '',
      quotedClientMessageId: reply.quotedClientMessageId || '',
      bagKeys: Object.keys(bag),
      chineseRuns: chineseRuns.slice(0, 6),
    };
  } catch {
    return { text: '', quotedName: '', quotedText: '', quotedSenderUid: '', quotedServerMessageId: '', quotedClientMessageId: '' };
  }
}

// Scan remaining protobuf fields for JSON / Chinese chat text when field 8 is empty
// or non-JSON. Image-reply frames may stash body outside classic content JSON.
// Also recover share-card title + reply body separately when both appear in nested fields.
function extractTextFromOtherFields(message, skipFields = new Set([1, 2, 3, 4, 5, 6, 7, 9, 10, 14])) {
  const preferredKeys = [
    'text', 'content', 'message', 'msg', 'title', 'description', 'content_title',
    'hint', 'body', 'richText', 'rich_text', 'display_text', 'displayText',
    'reply_text', 'replyText', 'comment', 'comment_text', 'commentText',
  ];
  const bodyCandidates = [];
  const quoteCandidates = [];
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
          const card = formatDouyinShareCardFromBag(parsed);
          if (card) quoteCandidates.push({ number, text: card, source: 'json-card' });
          if (nested && !/^[a-z][a-z0-9_]*$/i.test(nested) && !looksLikeDouyinShareCardText(nested)) {
            bodyCandidates.push({ number, text: nested, source: 'json' });
          } else if (nested && looksLikeDouyinShareCardText(nested)) {
            quoteCandidates.push({ number, text: compactDouyinShareQuote(nested), source: 'json-share' });
          }
          continue;
        }
      } catch {}
      // Nested protobuf inside this field.
      const nestedPb = extractTextFromNestedProtobuf(value);
      if (nestedPb.quotedText) {
        quoteCandidates.push({ number, text: nestedPb.quotedText, source: 'nested-pb-quote' });
      }
      if (nestedPb.text) {
        if (looksLikeDouyinShareCardText(nestedPb.text)) {
          quoteCandidates.push({ number, text: compactDouyinShareQuote(nestedPb.text), source: 'nested-pb-share' });
        } else {
          bodyCandidates.push({ number, text: nestedPb.text, source: 'nested-pb' });
        }
        continue;
      }
      const runs = extractPrintableRuns(value, 2, 8)
        .filter((run) => /[\u4e00-\u9fff]/.test(run) && run.length <= 200 && !/^[a-z][a-z0-9_]*$/i.test(run));
      for (const run of runs) {
        if (looksLikeDouyinShareCardText(run) || run.includes('分享图文') || run.includes('分享')) {
          const card = run.startsWith('[') ? compactDouyinShareQuote(run) : `[分享图文] ${run.replace(/^分享图文[:：\s]*/, '').slice(0, 40)}`;
          quoteCandidates.push({ number, text: card, source: 'raw-share' });
        } else {
          bodyCandidates.push({ number, text: run, source: 'raw' });
        }
      }
    }
  }
  bodyCandidates.sort((a, b) => b.text.length - a.text.length);
  quoteCandidates.sort((a, b) => b.text.length - a.text.length);
  return {
    text: bodyCandidates[0]?.text || '',
    quotedText: quoteCandidates[0]?.text || '',
    fieldHits: [
      ...bodyCandidates.slice(0, 4).map((c) => `b${c.number}:${c.source}:${c.text.slice(0, 40)}`),
      ...quoteCandidates.slice(0, 4).map((c) => `q${c.number}:${c.source}:${c.text.slice(0, 40)}`),
    ],
  };
}

// Recover sparse video-share JSON from non-field-8 protobuf blobs (esp. field 17).
function recoverVideoShareFromOtherFields(message) {
  const content = {};
  const fieldHits = [];
  for (const [number, values] of message.entries()) {
    if (number === 8 || number === 9) continue; // main content / ext map already handled
    for (const value of values || []) {
      if (!(value instanceof Uint8Array) || value.length < 8 || value.length > 200_000) continue;
      // Try UTF-8 JSON first
      let asText = '';
      try { asText = textDecoder.decode(value); } catch { asText = ''; }
      const trimmed = asText.trim();
      if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
        try {
          const parsed = JSON.parse(trimmed);
          if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
            Object.assign(content, parsed);
            fieldHits.push(`${number}:json`);
            continue;
          }
        } catch {}
      }
      // Nested protobuf → look for JSON leaf strings / long digit ids / https URLs
      try {
        const nested = decodeFields(value);
        for (const [n2, vals2] of nested.entries()) {
          for (const v2 of vals2 || []) {
            if (!(v2 instanceof Uint8Array)) continue;
            let t2 = '';
            try { t2 = textDecoder.decode(v2).trim(); } catch { continue; }
            if (!t2) continue;
            if ((t2.startsWith('{') || t2.startsWith('[')) && t2.length > 20) {
              try {
                const p2 = JSON.parse(t2);
                if (p2 && typeof p2 === 'object' && !Array.isArray(p2)) {
                  Object.assign(content, p2);
                  fieldHits.push(`${number}.${n2}:json`);
                }
              } catch {}
            }
            const idMatch = t2.match(/\/video\/(\d{8,})/i) || t2.match(/"(?:itemId|item_id|aweme_id|awemeId)"\s*:\s*"?(\d{8,})/i);
            if (idMatch && !content.itemId && !content.item_id && !content.aweme_id) {
              content.itemId = idMatch[1];
              fieldHits.push(`${number}.${n2}:itemId`);
            }
            if (/^https?:\/\//i.test(t2) && /cover|thumb|\.(jpg|jpeg|png|webp)/i.test(t2) && !content.cover_url) {
              content.cover_url = t2;
              fieldHits.push(`${number}.${n2}:cover`);
            }
          }
        }
      } catch {}
      // Brute printable runs for video id URLs inside binary
      const runs = extractPrintableRuns(value, 8, 12);
      for (const run of runs) {
        const m = run.match(/\/video\/(\d{8,})/i);
        if (m && !content.itemId) {
          content.itemId = m[1];
          fieldHits.push(`${number}:run-itemId`);
        }
        if (/^https?:\/\//i.test(run) && /cover|thumb|\.(jpg|jpeg|png|webp)/i.test(run) && !content.cover_url) {
          content.cover_url = run;
          fieldHits.push(`${number}:run-cover`);
        }
      }
    }
  }
  return { content, fieldHits: fieldHits.slice(0, 12) };
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
  let fieldHits = [];
  const fieldNumbers = [...message.keys()].sort((a, b) => a - b);

  // Private type-8 video shares often have EMPTY field 8 JSON (contentLen=0).
  // Scan other length-delimited protobuf fields for nested JSON / video ids / covers.
  if (
    (messageType === 8 || messageType === 77 || messageType === 105)
    && (!contentBytes || contentBytes.length === 0 || Object.keys(content).length === 0)
  ) {
    const recovered = recoverVideoShareFromOtherFields(message);
    if (recovered.content && Object.keys(recovered.content).length) {
      content = { ...content, ...recovered.content };
      contentParseOk = true;
    }
    if (recovered.fieldHits?.length) fieldHits = recovered.fieldHits;
  }

  const contentKeyList = content && typeof content === 'object' ? Object.keys(content) : [];
  let text = typeof content?.text === 'string' ? content.text.trim() : '';
  const replyFromContent = replyDetails(content, ext);
  let quotedName = replyFromContent.quotedName;
  let quotedText = replyFromContent.quotedText;
  let quotedSenderUid = replyFromContent.quotedSenderUid;
  let quotedServerMessageId = replyFromContent.quotedServerMessageId || '';
  let quotedClientMessageId = replyFromContent.quotedClientMessageId || '';
  let link = '';

  // Control / conv-biz commands never carry chat body.
  if (isDouyinControlCommand(messageType, content, ext)) {
    text = '[控制消息]';
  } else if (isDouyinVideoShare(messageType, content)) {
    // Video shares must be classified before firstText walks content_name (author).
    let video = formatDouyinVideoShare(content, ext);
    // Sparse type-8 private shares: dig more bags / other pb fields when card is bare.
    if (
      (video.text === '[视频]' || !video.link)
      && (messageType === 8 || messageType === 77 || messageType === 105)
    ) {
      const other = extractTextFromOtherFields(message);
      if (other.fieldHits?.length) fieldHits = [...fieldHits, ...other.fieldHits];
      if (!video.itemId && other.text) {
        const m = String(other.text).match(/\/video\/(\d{8,})/i) || String(other.text).match(/\b(\d{15,})\b/);
        if (m) {
          video = {
            ...video,
            itemId: m[1],
            link: video.link || `https://www.douyin.com/video/${m[1]}`,
          };
        }
      }
      if (video.text === '[视频]' && other.text && !looksLikeDouyinShareCardText(other.text) && /[\u4e00-\u9fff]/.test(other.text)) {
        const t = String(other.text).trim().slice(0, 80);
        if (t && t.length >= 2) video = { ...video, text: `[视频] ${t}` };
      }
    }
    text = video.text;
    link = video.link;
    if (video.cover) {
      content.__douyin_video_cover = video.cover;
    }
    if (text === '[视频]' && !link && !video.cover) {
      content.__douyin_video_sparse = true;
    }
  } else if (isLightInteractionMessage(messageType, content, ext)) {
    text = extractLightInteractionLabel(content, ext) || '[表情]';
  } else if (isDouyinSystemNotice(messageType, content)) {
    // Keep a short label for logs; mark as internal so QQ does not forward machine scenes.
    text = formatDouyinSystemNotice(content);
  } else if (messageType === 110) {
    text = formatDouyinType110(content, ext);
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
    // Also recovers reply body + video quote separately when both appear.
    {
      const needBody = !text;
      const needQuote = !quotedText;
      if (needBody || needQuote) {
        const other = extractTextFromOtherFields(message);
        if (other.fieldHits?.length) fieldHits = other.fieldHits;
        if (needQuote && other.quotedText) {
          quotedText = compactDouyinShareQuote(other.quotedText) || other.quotedText;
        }
        if (needBody && other.text) {
          if (!quotedText && looksLikeDouyinShareCardText(other.text)) {
            quotedText = compactDouyinShareQuote(other.text);
            // Leave body empty so we don't attribute the share card to the peer.
          } else if (!quotedText && (quotedServerMessageId || quotedClientMessageId || quotedName)
            && looksLikeDouyinShareCardText(other.text)) {
            quotedText = compactDouyinShareQuote(other.text);
          } else if (!looksLikeDouyinShareCardText(other.text)) {
            text = other.text;
          }
        }
      }
    }
    if (!quotedText && looksLikeDouyinShareCardText(text) && !contentParseOk && messageType === 7) {
      // Body-only path also picked share card from nested pb with empty JSON content.
      // Prefer quote stack: 「我/对方：[分享图文]/[视频]」 + real reply (or [回复]).
      quotedText = compactDouyinShareQuote(text);
      text = '';
    }
    // Nested content itself may be a video card used as quote source while body is reply text.
    if (!quotedText && contentParseOk && content && typeof content === 'object') {
      const card = formatDouyinShareCardFromBag(content);
      // Only promote to quote when this frame also looks like a reply (has ref or body separate).
      if (card && (quotedServerMessageId || quotedClientMessageId || quotedName || (text && text !== card))) {
        // If main text is empty and content is purely a card, keep as body (direct share), not quote.
        if (text) quotedText = card;
      }
    }
    text = formatDouyinStickerText(text, messageType);
  }

  // If we still have no body but replyDetails found a quote, surface a reply placeholder
  // so QQ at least shows "replied to X" instead of 暂不支持 — only when quote exists.
  if (!text && (quotedText || quotedName || quotedServerMessageId || quotedClientMessageId)) {
    text = '[回复]';
  }

  // Caption + shared video (common private share: type=8 empty card + type=7 caption JSON
  // with related_share_video.itemId). Keep the caption; attach the video link for QQ.
  // Do NOT rewrite body to bare [视频] — that loses the user's text.
  if (!link && content && typeof content === 'object') {
    const sharedId = extractDouyinItemId(content, ext);
    const related = content.related_share_video || content.relatedShareVideo || null;
    const relatedId = sharedId
      || String(related?.itemId || related?.item_id || related?.aweme_id || related?.awemeId || '').trim();
    if (relatedId) {
      link = `https://www.douyin.com/video/${relatedId}`;
      const cover = firstCoverURL(content) || firstCoverURL(related || {});
      if (cover) content.__douyin_video_cover = cover;
      // If body is empty but this is clearly a share-with-caption frame, keep a short label.
      if (!text && (messageType === 7 || content.aweType === 700 || content.share_id)) {
        text = formatDouyinVideoShare(content, ext).text || '[视频]';
      }
    }
  }

  // System notices / control metadata should not be forwarded to QQ.
  const internalMetadata = /^\d+:\d+:\d+:\d+$/.test(text)
    || isDouyinSystemNotice(messageType, content)
    || isDouyinControlCommand(messageType, content, ext)
    || text === 'go_to_maya_notice'
    || text === '[控制消息]';
  // Extract image URLs for type 27 (and sticker/emoji frames when present).
  let images = extractImageURLs(content, ext, messageType);
  if (isDouyinVideoShare(messageType, content)) {
    // Video cards: only keep the best cover, not every CDN mirror in the payload.
    const cover = content.__douyin_video_cover || firstCoverURL(content);
    images = cover ? [cover] : pickBestStickerURLs(images, 1);
  } else if (content?.__douyin_video_cover) {
    // Caption+share (type 7 with related_share_video): prefer one cover if present.
    images = [content.__douyin_video_cover];
  } else if ([5, 50002, 70002].includes(messageType) || isLightInteractionMessage(messageType, content, ext)) {
    for (const url of extractStickerImageURLs(content, ext)) {
      if (!images.includes(url)) images.push(url);
    }
    // Light-interaction / emoji: one image is enough (avoid static+animate double send).
    images = pickBestStickerURLs(images, 1);
  }
  if (messageType === 27) {
    if (!text) text = '[图片]';
    if (text === '[暂不支持的消息]') text = '[图片]';
  }
  if (!text) {
    text = ({
      5: '[表情]',
      17: '[语音]',
      27: '[图片]',
      110: '[你还没有喂精灵～]',
      50002: '[表情]',
      70002: '[表情]',
    })[messageType] || '[暂不支持的消息]';
  }
  // If content carried image URLs but type was not 27, still surface them (rare).
  if (images.length === 0 && (messageType === 27 || /url_list|cover_url|image_url|static_url|animate_url/i.test(JSON.stringify(content || {}).slice(0, 500)))) {
    images = extractImageURLs(content, ext, messageType);
  }
  // When sticker only has image, keep a short caption ONLY if no image URL resolved.
  if (!text && images.length > 0 && [5, 27, 50002, 70002].includes(messageType)) {
    text = messageType === 27 ? '[图片]' : '[表情]';
  }
  // Sticker/emoji with real image: drop [表情]/[早点睡]/[比心] captions (not [视频]/chat text).
  if (
    images.length > 0
    && ([5, 50002, 70002].includes(messageType) || isLightInteractionMessage(messageType, content, ext))
    && isStickerCaptionText(text)
  ) {
    text = '';
  }
  // contentKeyList is captured after sparse recovery above.
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
    images,
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
  formatDouyinType110,
  isDouyinControlCommand,
  isDouyinSystemNotice,
  looksLikeDouyinShareCardText,
};

export const douyinIMInternals = { decodeFields, asString, asNumber };
