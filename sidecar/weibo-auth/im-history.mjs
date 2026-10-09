// 抖音 IM 补全（HTTP 侧）。
//
// 本文件的协议参数全部来自**对抖音当前 web IM 页面的真实抓包**（2026-10-02），
// 不是照搬任何第三方项目。抓包方式：Playwright `ctx.route('**/*')` 拦截
// imapi.douyin.com，用 route.fetch() 取响应后 route.fulfill 原样放行，
// 同时拿到请求体与响应体的完整字节。
//
// 页面实际调用三个接口，共同包体骨架：
//   #1 cmd / #2 seq / #3 sdk="0.1.8" / #4 空 / #5 refer=3 / #6=1
//   #7 store="0d50935:feat/pc-im-group" / #8 查询体 / #9="0"
//   #11="douyin_pc" / #14="360000" / #15×18设备指纹 / #18=1
//   #21="douyin_web" / #22="web_sdk"
// 其中 #21 必须是 douyin_web（早期误用 douyin_pc 会得到 conversation not found）。
//   - get_conversation_list  cmd=1001 seq=10004  → 活跃会话列表（拿真实 convId）
//   - get_message_by_init    cmd=2043 seq=10002
//   - get_user_message       cmd=2048 seq=10005  → 拉消息
//
// 响应约定：#3 status（0 成功）、#4 error_desc（"OK"/业务错误）、#5=1、
// #7=logId、#10/#11=毫秒时间戳、#13=自己的 uid、#6=载荷（需再解一层）。
// 连续请求会返回 "too many requests"，调用方需自行限速。

// ── protobuf 最小编解码 ────────────────────────────────────────
function encodeVarint(value) {
  const bytes = [];
  let v = typeof value === 'bigint' ? value : BigInt(value);
  do {
    let b = Number(v & 0x7Fn);
    v >>= 7n;
    if (v > 0n) b |= 0x80;
    bytes.push(b);
  } while (v > 0n);
  if (bytes.length === 0) bytes.push(0);
  return new Uint8Array(bytes);
}

function encodeTag(fn, wt) {
  return encodeVarint((fn << 3) | wt);
}

function encodeString(fn, s) {
  const e = new TextEncoder().encode(s);
  return concatArrays([encodeTag(fn, 2), encodeVarint(e.length), e]);
}

function encodeVarintField(fn, v) {
  return concatArrays([encodeTag(fn, 0), encodeVarint(v)]);
}

function encodeBytes(fn, d) {
  return concatArrays([encodeTag(fn, 2), encodeVarint(d.length), d]);
}

function concatArrays(arrs) {
  const total = arrs.reduce((s, a) => s + a.length, 0);
  const out = new Uint8Array(total);
  let o = 0;
  for (const a of arrs) {
    out.set(a, o);
    o += a.length;
  }
  return out;
}

// server_message_id 是 snowflake（>2^53），必须走 BigInt，否则精度丢失导致对齐失败。
function decodeVarint(buf, pos) {
  let result = 0n;
  let shift = 0n;
  while (pos < buf.length) {
    const b = buf[pos++];
    result += BigInt(b & 0x7F) << shift;
    if ((b & 0x80) === 0) break;
    shift += 7n;
    if (shift > 70n) break;
  }
  return [result, pos];
}

function extractField(buf, target) {
  let pos = 0;
  while (pos < buf.length) {
    let tag;
    [tag, pos] = decodeVarint(buf, pos);
    const fn = Number(tag >> 3n);
    const wt = Number(tag & 7n);
    if (fn === 0) break;
    if (wt === 0) {
      [, pos] = decodeVarint(buf, pos);
    } else if (wt === 2) {
      let lenRaw;
      [lenRaw, pos] = decodeVarint(buf, pos);
      const len = Number(lenRaw);
      if (fn === target) return buf.slice(pos, pos + len);
      pos += len;
    } else if (wt === 1) pos += 8;
    else if (wt === 5) pos += 4;
    else break;
  }
  return null;
}

function parseProtoShallow(buf) {
  const out = {};
  let pos = 0;
  while (pos < buf.length) {
    let tag;
    [tag, pos] = decodeVarint(buf, pos);
    const fn = Number(tag >> 3n);
    const wt = Number(tag & 7n);
    if (fn === 0 || fn > 500) break;
    if (wt === 0) {
      let v;
      [v, pos] = decodeVarint(buf, pos);
      out['f' + fn] = v.toString();
    } else if (wt === 2) {
      let lenRaw;
      [lenRaw, pos] = decodeVarint(buf, pos);
      const len = Number(lenRaw);
      if (pos + len > buf.length) break;
      const slice = buf.slice(pos, pos + len);
      try {
        out['f' + fn] = new TextDecoder('utf-8', { fatal: true }).decode(slice);
      } catch {
        out['f' + fn] = slice;
      }
      pos += len;
    } else if (wt === 1) pos += 8;
    else if (wt === 5) pos += 4;
    else break;
  }
  return out;
}

// ── 协议常量（全部来自真实抓包）────────────────────────────
import fs from 'node:fs';
import pathMod from 'node:path';

const API_BASE = 'https://imapi.douyin.com';
const SDK_VERSION = '0.1.8';
const STORE_KEY = '0d50935:feat/pc-im-group';
const CLIENT_VERSION = '360000';
const APP_NAME = 'douyin_pc';
const PLATFORM = 'douyin_web'; // #21 必须是 douyin_web
const ACCESS = 'web_sdk';          // #22

const CMD_CONVERSATION_LIST = 1001;
const CMD_USER_MESSAGE = 2048;

// 设备指纹（#15）。浏览器相关的必须实时取，静态写死会被判异常。
const DEVICE_FINGERPRINT_KEYS = [
  'session_aid', 'session_did', 'app_name', 'priority_region', 'user_agent',
  'cookie_enabled', 'browser_language', 'browser_platform', 'browser_name',
  'browser_version', 'browser_online', 'screen_width', 'screen_height',
  'referer', 'timezone_name', 'deviceId', 'is-retry',
];

/** 在页面内采集设备指纹。 */
async function collectDeviceFingerprint(page) {
  return page.evaluate((keys) => {
    const ua = navigator.userAgent;
    const out = {};
    for (const k of keys) {
      switch (k) {
        case 'session_aid': out[k] = '6383'; break;
        case 'session_did': out[k] = '0'; break;
        case 'app_name': out[k] = 'douyin_pc'; break;
        case 'priority_region': out[k] = 'cn'; break;
        case 'user_agent': out[k] = ua; break;
        case 'cookie_enabled': out[k] = 'true'; break;
        case 'browser_language': out[k] = navigator.language || 'zh-CN'; break;
        case 'browser_platform': out[k] = navigator.platform || ''; break;
        case 'browser_name': out[k] = /Firefox/i.test(ua) ? 'Firefox' : (/Edg\//.test(ua) ? 'Edge' : 'Mozilla'); break;
        case 'browser_version': out[k] = ua; break;
        case 'browser_online': out[k] = 'true'; break;
        case 'screen_width': out[k] = String(window.innerWidth || screen.width || 1920); break;
        case 'screen_height': out[k] = String(window.innerHeight || screen.height || 1080); break;
        case 'referer': out[k] = document.referrer || ''; break;
        case 'timezone_name': out[k] = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai'; break;
        case 'deviceId': out[k] = '0'; break;
        case 'is-retry': out[k] = '0'; break;
        default: out[k] = '';
      }
    }
    return out;
  }, DEVICE_FINGERPRINT_KEYS);
}

/** 组装页面一致的公共包体。query 为接口各自的 #8 载荷。 */
function buildEnvelope(cmd, seq, query, fingerprint) {
  const parts = [
    encodeVarintField(1, cmd),
    encodeVarintField(2, seq),
    encodeString(3, SDK_VERSION),
    encodeBytes(4, new Uint8Array(0)),
    encodeVarintField(5, 3),
    encodeVarintField(6, 1),
    encodeString(7, STORE_KEY),
    encodeBytes(8, query),
    encodeString(9, '0'),
    encodeString(11, APP_NAME),
    encodeString(14, CLIENT_VERSION),
  ];
  for (const key of DEVICE_FINGERPRINT_KEYS) {
    const value = fingerprint && fingerprint[key] !== undefined ? String(fingerprint[key]) : '';
    parts.push(encodeBytes(15, concatArrays([encodeString(1, key), encodeString(2, value)])));
  }
  parts.push(encodeVarintField(18, 1));
  parts.push(encodeString(21, PLATFORM));
  parts.push(encodeString(22, ACCESS));
  return concatArrays(parts);
}

// ── 会话列表（拿当前活跃 convId，优于从旧帧里翻）─────────────
// 抓包显示 #8 载荷 = field1000{ #1=0 #2=1 #3=20 }（共 9 字节）。
// 早期误用 field301/空载荷，服务端会回
// "request.GetStrangerConversation is empty (status=4)"。
function buildConversationListQuery() {
  const inner = concatArrays([
    encodeVarintField(1, 0),
    encodeVarintField(2, 1),
    encodeVarintField(3, 20),
  ]);
  return encodeBytes(1000, inner);
}

/**
 * fetchConversations 返回当前活跃会话列表。
 * 元素形如 { conversationId, name, cursor, lastMessageTime }。
 */
export async function fetchConversations(page) {
  if (!page || page.isClosed()) return { conversations: [], failed: true, error: 'page 不可用' };
  const fingerprint = await collectDeviceFingerprint(page);
  const body = buildEnvelope(CMD_CONVERSATION_LIST, 10004, buildConversationListQuery(), fingerprint);
  const r = await postProtobuf(page, `${API_BASE}/v1/stranger/get_conversation_list`, body);
  if (r.failed) return { conversations: [], failed: true, error: r.error };
  return { conversations: r.conversations || [], failed: false, error: '' };
}

// ── 拉消息正文 ───────────────────────────────────────────────
// 抓包结论：消息正文在 `get_message_by_init`（cmd=2043）的响应里，结构为
//   响应 #6 → #2043 → #1(会话数组) → #2[](该会话的消息数组)
// 消息字段：#3=server_id #6=type #7=sender_uid #8=content_json
//           #14=sender_sec_uid #18=引用 JSON
// 单次响应可达 ~2MB，因此只在"推送帧正文是占位符"时才调用。
// 注意：`get_user_message`（cmd=2048）**不是**拉正文用的，它的 #8 只有时间戳+游标。
const CMD_MESSAGE_BY_INIT = 2043;
const SEQ_MESSAGE_BY_INIT = 10001;

// 短 TTL 缓存：一次拉取覆盖全量消息，避免同一时间窗内重复请求被限流。
const CACHE_TTL_MS = 60_000;
let messageCache = { at: 0, messages: [], index: new Map() };

// 失败退避：抖音对同一账号的高频拉取会触发风控。补正文失败后必须等一段时间
// 再试，否则连续几次都在被拦的窗口里，白白浪费消息（实测 35 秒内重试两次
// 均被拦）。这里按连续失败次数指数退避，最长 5 分钟。
const FAIL_BACKOFF_BASE_MS = 45_000;
const FAIL_BACKOFF_MAX_MS = 300_000;
let consecutiveFailures = 0;
let nextAttemptAt = 0;

function buildMessageByInitQuery() {
  // 真实抓包（2026-10-02 23:57，route.fetch 拦截且原样放行）对比同一接口的两次调用：
  //   req 693B -> resp 1,831,829B   #2043{ f2: 0 }              ← 首次引导，带全部消息
  //   req 702B -> resp        89B   #2043{ f1:<ms>, f2: 1 }     ← 增量轮询，空列表
  // 之前误用了后者（f1 塞 Date.now()），服务端返回 status=0/"OK" 但消息为空，
  // 于是每条占位消息都补不上正文。补正文要的是「全部近期消息」，因此只发 { f2: 0 }。
  return encodeBytes(2043, encodeVarintField(2, 0));
}

/**
 * fetchAllRecentMessages 拉取当前账号的近期消息（覆盖私聊 + 群聊）。
 * 按 server_message_id 建立索引，供补正文时对齐推送帧。
 */
export async function fetchAllRecentMessages(page) {
  const ok = { messages: [], failed: false, error: '' };
  if (!page || page.isClosed()) return { ...ok, failed: true, error: 'page 不可用' };
  const now = Date.now();
  if (now - messageCache.at < CACHE_TTL_MS && messageCache.messages.length > 0) {
    return { messages: messageCache.messages, failed: false, error: '', cached: true };
  }
  // 退避窗口内不再重复触发请求：与其连撞风控，不如等窗口过后一次性补上。
  if (nextAttemptAt && now < nextAttemptAt) {
    return {
      ...ok,
      failed: true,
      error: `风控退避中，${Math.ceil((nextAttemptAt - now) / 1000)}s 后再试`,
    };
  }
  const fingerprint = await collectDeviceFingerprint(page);
  const body = buildEnvelope(CMD_MESSAGE_BY_INIT, SEQ_MESSAGE_BY_INIT, buildMessageByInitQuery(), fingerprint);
  const r = await postProtobuf(page, `${API_BASE}/v1/message/get_message_by_init`, body);
  if (r.failed) {
    consecutiveFailures += 1;
    const wait = Math.min(
      FAIL_BACKOFF_BASE_MS * 2 ** (consecutiveFailures - 1),
      FAIL_BACKOFF_MAX_MS,
    );
    nextAttemptAt = Date.now() + wait;
    return { ...ok, failed: true, error: `${r.error}（退避 ${Math.round(wait / 1000)}s）` };
  }
  consecutiveFailures = 0;
  nextAttemptAt = 0;
  const messages = r.messages || [];
  const index = new Map();
  for (const m of messages) {
    if (m.serverId) index.set(String(m.serverId), m);
  }
  messageCache = { at: now, messages, index };
  return { messages, failed: false, error: '' };
}

/** lookupByServerId 在短 TTL 缓存里按 server_message_id 查消息。 */
export function lookupByServerId(serverId) {
  if (!serverId) return null;
  const hit = messageCache.index.get(String(serverId));
  if (!hit) return null;
  if (Date.now() - messageCache.at > CACHE_TTL_MS) return null;
  return hit;
}

/**
 * fetchRecentMessages 兼容旧签名：忽略 convId，改为按 serverId 或时间窗回填。
 */
export async function fetchRecentMessages(page, convId, sinceTimestampSec) {
  return fetchAllRecentMessages(page);
}

// ── 页面内 POST + 响应解包 ──────────────────────────────────
async function postProtobuf(page, path, body) {
  let resp;
  try {
    // 用 Playwright 自己的请求上下文（APIRequestContext）而不是页面里的
    // window.fetch：抖音的安全 SDK 会劫持页面内 fetch，在请求密集时直接抛
    // "TypeError: Failed to fetch"，补正文因此全军覆没。APIRequestContext 走
    // Node 侧的 Chromium 网络栈，不经过页面 JS，因此不会被拦。
    // 认证靠浏览器上下文里已有的 Cookie（APIRequestContext 与浏览器共享）。
    const api = page.context().request;
    const url = `${path}?aid=6383&device_platform=webapp`;
    const response = await api.post(url, {
      headers: {
        'Content-Type': 'application/x-protobuf',
        Accept: 'application/x-protobuf',
        Referer: 'https://www.douyin.com/chat',
        Origin: 'https://www.douyin.com',
      },
      data: Buffer.from(body),
      timeout: 25000,
      failOnStatusCode: false,
    });
    resp = { status: response.status(), bytes: Array.from(await response.body()) };
  } catch (err) {
    return { failed: true, error: (err && err.message) || String(err) };
  }
  if (!resp || resp.status !== 200) {
    return { failed: true, error: `HTTP ${resp ? resp.status : 'no-response'}` };
  }
  if (!Array.isArray(resp.bytes) || resp.bytes.length === 0) {
    return { failed: true, error: '空响应体' };
  }
  dumpRawResponse(path, resp.bytes);
  try {
    return unwrapEnvelope(new Uint8Array(resp.bytes));
  } catch (err) {
    return { failed: true, error: `响应解析失败: ${(err && err.message) || err}` };
  }
}

// 诊断用：设置 POCKET48_DYIM_DUMP=1 时把原始响应写到 storage/douyin-im-dumps/。
// 解析层数对不上时，只有真实字节能说明当前结构。
function dumpRawResponse(path, bytes) {
  if (!process.env.POCKET48_DYIM_DUMP) return;
  try {
    const dir = '/root/pocket48-bot/storage/douyin-im-dumps';
    fs.mkdirSync(dir, { recursive: true });
    const name = `${pathMod.basename(path)}_${Date.now()}_${bytes.length}B`;
    const buf = Buffer.from(bytes);
    fs.writeFileSync(`${dir}/${name}.bin`, buf);
    fs.writeFileSync(`${dir}/${name}.hex.txt`, buf.toString('hex'));
    // 浅层字段号一览，便于确认层级是否变化
    const top = parseProtoShallow(new Uint8Array(bytes));
    const summary = Object.entries(top)
      .map(([k, v]) => `${k}=${typeof v === 'string' ? v.slice(0, 40) : Array.isArray(v) ? `[${v.length}]` : v}`)
      .join(' ');
    fs.writeFileSync(`${dir}/${name}.top.txt`, `${summary}\n`);
  } catch (err) {
    // 诊断功能永远不能影响主流程
  }
}

/** 解外层信封：#3 status / #4 error_desc，其余交给载荷解析。 */
function unwrapEnvelope(buffer) {
  const top = parseProtoShallow(buffer);
  const status = Number(top.f3 || 0);
  const errorDesc = typeof top.f4 === 'string' ? top.f4.trim() : '';
  if (errorDesc && errorDesc !== 'OK') {
    return { failed: true, error: `${errorDesc} (status=${status})` };
  }
  if (status !== 0 && !errorDesc) {
    return { failed: true, error: `status=${status}` };
  }
  const payload = extractField(buffer, 6);
  if (!payload || payload.length === 0) {
    return { messages: [], failed: false, error: '' };
  }
  // 抓包层级：#6 → #2043 → #1(会话)[] → #2(消息)[]
  const body = extractField(payload, 2043) || payload;
  const messages = [];
  for (const conv of readRepeated(body, 1)) {
    for (const raw of readRepeated(conv, 2)) {
      const msg = parseHistoryMessage(raw);
      if (msg) messages.push(msg);
    }
  }
  return { messages, failed: false, error: '' };
}

/** 读某字段号下所有 length-delimited 值。 */
function readRepeated(buf, fieldNum) {
  const out = [];
  let pos = 0;
  while (pos < buf.length) {
    let tag;
    [tag, pos] = decodeVarint(buf, pos);
    const fn = Number(tag >> 3n);
    const wt = Number(tag & 7n);
    if (fn === 0) break;
    if (wt === 0) {
      [, pos] = decodeVarint(buf, pos);
    } else if (wt === 2) {
      let lenRaw;
      [lenRaw, pos] = decodeVarint(buf, pos);
      const len = Number(lenRaw);
      if (pos + len > buf.length) break;
      if (fn === fieldNum) out.push(buf.slice(pos, pos + len));
      pos += len;
    } else if (wt === 1) pos += 8;
    else if (wt === 5) pos += 4;
    else break;
  }
  return out;
}

function parseHistoryMessage(buf) {
  const f = parseProtoShallow(buf);
  const serverId = f.f3 || '';
  const contentJson = typeof f.f8 === 'string' ? f.f8 : '';
  if (!serverId && !contentJson) return null;
  const out = {
    conversationId: typeof f.f1 === 'string' ? f.f1 : '',
    serverId,
    createdAtUs: f.f4 || '',
    typeCode: Number(f.f6 || 0),
    senderUid: f.f7 || '',
    contentJson,
    senderSecUid: typeof f.f14 === 'string' ? f.f14 : '',
    text: '',
    aweType: -1,
    imageUrl: '',
    refMessage: null,
  };
  // f18 引用/回复：内层 f1=被引用 server_id，f2=JSON
  if (f.f18 && f.f18 instanceof Uint8Array && f.f18.length > 0) {
    const ref = parseProtoShallow(f.f18);
    if (ref.f2 && typeof ref.f2 === 'string') {
      try {
        const rj = JSON.parse(ref.f2);
        out.refMessage = {
          serverId: ref.f1 || '',
          content: rj.content || '',
          nickname: rj.nickname || '',
          secUid: rj.refmsg_sec_uid || '',
          refMsgType: Number(rj.refmsg_type || 0),
          refMsgContent: rj.refmsg_content || '',
        };
      } catch {}
    }
  }
  if (contentJson) {
    try {
      const cj = JSON.parse(contentJson);
      out.aweType = Number(cj.aweType ?? -1);
      out.text = cj.text || cj.description || '';
      const ru = cj.resource_url;
      if (ru && typeof ru === 'object') {
        for (const key of ['large_url_list', 'origin_url_list', 'medium_url_list', 'thumb_url_list']) {
          if (Array.isArray(ru[key]) && typeof ru[key][0] === 'string') {
            out.imageUrl = ru[key][0];
            break;
          }
        }
      }
    } catch {}
  }
  const sid = Number(out.serverId || 0);
  out.timestampSec = sid > 0 ? Math.floor(sid / 2 ** 32) : 0;
  return out;
}

// ── 昵称/头像补全（已实测可用）──────────────────────────────
const USER_INFO_API = 'https://www.douyin.com/aweme/v1/web/im/user/info/';
const BATCH_USER_INFO = 20;

/**
 * fetchUserProfiles 按 sec_uid 批量补昵称/头像。
 * 推送帧只带数字 uid，没有昵称；该接口只认 cookies，无需签名。
 */
export async function fetchUserProfiles(page, secUids) {
  const ids = [...new Set((secUids || []).filter((v) => typeof v === 'string' && v))];
  if (!page || page.isClosed() || ids.length === 0) return {};
  const out = {};
  for (let i = 0; i < ids.length; i += BATCH_USER_INFO) {
    const batch = ids.slice(i, i + BATCH_USER_INFO);
    try {
      const resp = await page.evaluate(async ({ url, ids: list }) => {
        const r = await fetch(url, {
          method: 'POST',
          credentials: 'include',
          headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
          body: 'sec_user_ids=' + encodeURIComponent(JSON.stringify(list)),
          signal: AbortSignal.timeout(15000),
        });
        return { status: r.status, text: await r.text() };
      }, { url: `${USER_INFO_API}?aid=6383&device_platform=webapp`, ids: batch });
      if (!resp || resp.status !== 200) continue;
      const payload = JSON.parse(resp.text);
      for (const item of payload?.data || []) {
        const nickname = item?.nickname || item?.unique_id || '';
        if (!nickname) continue;
        const avatar = item?.avatar_thumb?.url_list?.[0] || item?.avatar_larger?.url_list?.[0] || '';
        if (item.user_id) out[item.user_id] = { nickname, avatar, secUid: item.sec_uid || '' };
        if (item.sec_uid) out[item.sec_uid] = { nickname, avatar, secUid: item.sec_uid };
      }
    } catch {
      // 单批失败不影响整体
    }
  }
  return out;
}

export {
  parseHistoryMessage,
  buildEnvelope,
  collectDeviceFingerprint,
  unwrapEnvelope,
  readRepeated,
};