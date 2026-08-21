import http from 'node:http';
import process from 'node:process';
import fs from 'node:fs';
import path from 'node:path';
import { WebSocket, WebSocketServer } from 'ws';
import { AndroidChatroomClient } from './android-chatroom.mjs';
import { AndroidQChatClient } from './android-qchat.mjs';

const APP_KEY = '632feff1f4c838541ab75195d1ceb3fa';

const portArg = process.argv.find((arg) => arg.startsWith('--wsPort='));
const requestedPort = Number(portArg?.slice('--wsPort='.length) || 0);
const server = http.createServer();
const wss = new WebSocketServer({ server });
const peers = new Set();
const liveRooms = new Map();
const qchatState = {
  client: undefined,
  credentialKey: '',
  connected: false,
  rooms: new Map()
};
let healthTimer;

function emit(type, fields = {}) {
  const payload = JSON.stringify({ type, ...fields });
  for (const peer of peers) {
    if (peer.readyState === WebSocket.OPEN) peer.send(payload);
  }
}

function log(msg) {
  process.stderr.write(`${JSON.stringify({ type: 'log', msg })}\n`);
}

function errorMessage(error) {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  try { return JSON.stringify(error); } catch { return String(error); }
}

function parseJSON(value) {
  if (value == null || value === '') return undefined;
  if (typeof value === 'object') return value;
  try {
    const parsed = JSON.parse(value);
    return typeof parsed === 'string' ? parseJSON(parsed) : parsed;
  } catch {
    return undefined;
  }
}

function firstValue(...values) {
  return values.find((value) => value !== undefined && value !== null && value !== '');
}

function normalizedUser(raw, custom = {}) {
  const user = custom.user || custom.member || custom.fromUser || {};
  return {
    from: String(firstValue(user.userId, user.account, raw.from, raw.from_id_, raw.from_account_, '') || ''),
    nick: String(firstValue(user.nickName, user.nickname, raw.fromNick, raw.from_nick_, '') || ''),
    avatar: String(firstValue(user.avatar, user.userAvatar, '') || '')
  };
}

function liveMessageType(raw) {
  return firstValue(raw.msg_type_, raw.type, raw.msgType);
}

function liveCustom(raw) {
  return parseJSON(firstValue(raw.msg_attach_, raw.msg_setting_?.ext_, raw.custom, raw.attach, raw.ext)) || {};
}

function findPositiveNumber(value, keys) {
  const wanted = new Map(keys.map((key, index) => [key.toLowerCase(), index]));
  let best;
  let bestIndex = Number.MAX_SAFE_INTEGER;
  let bestPath = '';
  const walk = (node, path = '') => {
    if (!node || typeof node !== 'object') return;
    if (Array.isArray(node)) {
      for (const child of node) walk(child, path);
      return;
    }
    for (const [key, child] of Object.entries(node)) {
      const nextPath = path ? `${path}.${key}` : key;
      const index = wanted.get(key.toLowerCase());
      const number = Number(child);
      if (index !== undefined && Number.isFinite(number) && number >= 0 && index < bestIndex) {
        // Prefer liveUpdateInfo.online over bare online elsewhere when same priority.
        best = number;
        bestIndex = index;
        bestPath = nextPath;
      }
      walk(child, nextPath);
    }
  };
  walk(value);
  return best;
}

function liveOnlineCount(custom) {
  // 2026-07-22 胡晓慧 live: liveUpdateInfo.online only rises (27→1646, 0 downs) → 人气/人次，
  // NOT fluctuating concurrent occupancy. Still use it for 「最高人气」 until we find a better field.
  // Prefer nested liveUpdateInfo.online over getLiveOne-style onlineNum when both exist.
  if (custom && typeof custom === 'object') {
    const nested = custom.liveUpdateInfo || custom.live_update_info || custom.liveUpdate;
    if (nested && nested.online != null) {
      const n = Number(nested.online);
      if (Number.isFinite(n) && n >= 0) return n;
    }
  }
  return findPositiveNumber(custom, [
    // still scan concurrent-ish names first in case platform adds them later
    'currentOnline', 'concurrentNum', 'concurrentOnline', 'realOnlineNum', 'realOnline',
    'onlineUserNum', 'onlineUsers', 'watchingCount', 'audienceCount', 'watcherCount',
    'liveOnlineNum', 'liveOnline', 'personNum', 'peopleNum', 'viewers',
    'online',
    'onlineCount', 'userCount', 'memberCount', 'onlineNum',
    'hotValue', 'popularity', 'popularityValue', 'heat', 'uv', 'pv',
  ]);
}

function collectPositiveNumberFields(value, out = {}, path = '', depth = 0) {
  if (value == null || depth > 6) return out;
  if (typeof value === 'number' && Number.isFinite(value) && value >= 0) {
    out[path || '(root)'] = value;
    return out;
  }
  if (typeof value === 'string' && /^-?\d+(\.\d+)?$/.test(value.trim())) {
    const n = Number(value);
    if (Number.isFinite(n) && n >= 0) out[path || '(root)'] = n;
    return out;
  }
  if (Array.isArray(value)) {
    value.slice(0, 20).forEach((item, i) => collectPositiveNumberFields(item, out, `${path}[${i}]`, depth + 1));
    return out;
  }
  if (typeof value === 'object') {
    for (const [key, child] of Object.entries(value)) {
      const next = path ? `${path}.${key}` : key;
      collectPositiveNumberFields(child, out, next, depth + 1);
    }
  }
  return out;
}

// Dump raw LIVEUPDATE JSON for offline analysis (find fluctuating concurrent fields).
// Rate-limited: first dump + then at most once per ~60s per room. Stored under storage/live-liveupdate-dumps/.
const liveUpdateDumpLastAt = new Map(); // roomId -> ms
const LIVEUPDATE_DUMP_INTERVAL_MS = 60_000;
const LIVEUPDATE_DUMP_DIR = path.resolve(process.cwd(), 'storage', 'live-liveupdate-dumps');

function dumpLiveUpdateRaw(binding, custom, raw) {
  try {
    const roomId = String(binding?.pocketRoomId || binding?.nimRoomId || 'unknown');
    const now = Date.now();
    const last = liveUpdateDumpLastAt.get(roomId) || 0;
    const isFirst = last === 0;
    if (!isFirst && now - last < LIVEUPDATE_DUMP_INTERVAL_MS) return;
    liveUpdateDumpLastAt.set(roomId, now);

    fs.mkdirSync(LIVEUPDATE_DUMP_DIR, { recursive: true });
    const liveId = String(
      custom?.liveUpdateInfo?.liveId
      || custom?.liveUpdateInfo?.live_id
      || custom?.sourceId
      || custom?.liveId
      || 'unknown',
    ).replace(/[^\w.-]/g, '_');
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    const file = path.join(LIVEUPDATE_DUMP_DIR, `room${roomId}_${liveId}_${stamp}.json`);
    const payload = {
      dumpedAt: new Date().toISOString(),
      pocketRoomId: binding?.pocketRoomId,
      nimRoomId: binding?.nimRoomId,
      messageType: 'LIVEUPDATE',
      // custom is the parsed attach used for metrics; raw keeps full chatroom envelope when present
      custom,
      raw: raw && typeof raw === 'object'
        ? {
            type: raw.type ?? raw.msg_type_ ?? raw.msgType,
            time: raw.time ?? raw.timetag_ ?? raw.msg_time_,
            // avoid huge binary buffers
            keys: Object.keys(raw),
            attachPreview: typeof raw.attach === 'string' ? raw.attach.slice(0, 4000) : undefined,
            customPreview: typeof raw.custom === 'string' ? raw.custom.slice(0, 4000) : undefined,
            msg_attach_type: typeof raw.msg_attach_,
            msg_setting_ext_type: typeof raw.msg_setting_?.ext_,
          }
        : null,
      numericFields: collectPositiveNumberFields(custom),
    };
    fs.writeFileSync(file, `${JSON.stringify(payload, null, 2)}\n`, 'utf8');
    console.error(`[nim-bridge] LIVEUPDATE raw dump room=${roomId} file=${file}${isFirst ? ' (first)' : ''}`);
  } catch (err) {
    console.error(`[nim-bridge] LIVEUPDATE dump failed: ${err?.message || err}`);
  }
}

const GIFT_DUMP_DIR = path.resolve(process.cwd(), 'storage', 'gift-raw-dumps');
let giftDumpCount = 0;
const GIFT_DUMP_MAX = 200;
function dumpGiftRaw(binding, custom, raw) {
  if (giftDumpCount >= GIFT_DUMP_MAX) return;
  try {
    fs.mkdirSync(GIFT_DUMP_DIR, { recursive: true });
    const roomId = String(binding?.pocketRoomId || binding?.nimRoomId || 'unknown');
    const gift = custom?.giftInfo || custom?.gift || custom;
    const giftName = String(gift?.giftName || gift?.name || custom?.giftName || 'unknown').replace(/[^0-9A-Za-z_\u4e00-\u9fa5.-]/g, '_').slice(0, 30);
    const stamp = new Date().toISOString().replace(/[:.]/g, '-');
    const file = path.join(GIFT_DUMP_DIR, `gift_room${roomId}_${giftName}_${stamp}_${giftDumpCount}.json`);
    const payload = { dumpedAt: new Date().toISOString(), pocketRoomId: binding?.pocketRoomId, nimRoomId: binding?.nimRoomId, custom, rawKeys: raw && typeof raw === 'object' ? Object.keys(raw) : [], numericFields: collectPositiveNumberFields(custom) };
    fs.writeFileSync(file, `${JSON.stringify(payload, null, 2)}\n`, 'utf8');
    giftDumpCount++;
    console.error(`[nim-bridge] GIFT-RAW dump #${giftDumpCount} room=${roomId} file=${file}`);
  } catch (err) {
    console.error(`[nim-bridge] GIFT dump failed: ${err?.message || err}`);
  }
}

function handleLiveMessages(binding, messages) {
  for (const raw of Array.isArray(messages) ? messages : [messages]) {
    const type = liveMessageType(raw);
    const custom = liveCustom(raw);
    const user = normalizedUser(raw, custom);
    const time = Number(firstValue(raw.time, raw.timetag_, raw.msg_time_, Date.now()));
    const base = { roomId: binding.pocketRoomId, nimRoomId: binding.nimRoomId };

    if (type === 0 || type === 'text') {
      const text = String(firstValue(raw.text, raw.body, raw.msg_body_, raw.content, '') || '');
      if (text) emit('danmaku', { ...base, data: { type: 'text', ...user, text, time } });
      continue;
    }

    if (type === 100 || type === 'custom') {
      const messageType = String(firstValue(custom.messageType, custom.type, '') || '').toUpperCase();
      const barrage = custom.barrageInfo || custom.barrage || custom;
      const text = firstValue(barrage.text, barrage.content, barrage.message, barrage.msg);
      if (messageType.includes('BARRAGE') && text) {
        emit('danmaku', { ...base, data: { type: 'barrage', ...user, text: String(text), time } });
      }

      const gift = custom.giftInfo || custom.giftInfoList || custom.gift;
      // --- GIFT dual probe: dump every gift custom to disk (survives log pipeline) ---
      const hasGiftPayload = !!(custom.giftInfo || custom.giftInfoList || custom.gift || String(messageType).includes('GIFT') || gift);
      if (hasGiftPayload) {
        try {
          const brief = JSON.stringify(custom).slice(0, 1200);
          console.error(`[nim-bridge] GIFT-RAW keys=${Object.keys(custom).join(',')} giftKeys=${gift ? Object.keys(gift).join(',') : '-'} customHead=${brief}`);
        } catch {}
        try { dumpGiftRaw(binding, custom, raw); } catch {}
      }
      if (gift) {

        emit('gift', {
          ...base,
          data: {
            ...user,
            giftName: String(firstValue(gift.giftName, gift.name, '礼物')),
            giftNum: Number(firstValue(gift.giftNum, gift.count, 1)),
            giftId: String(firstValue(gift.giftId, gift.id, '')),
            receiver: String(firstValue(gift.acceptUser?.userName, gift.acceptUserName, gift.receiverName, '')),
            raw: custom,
            time
          }
        });
      }

      if (messageType === 'LIVEUPDATE') {
        const onlineNum = liveOnlineCount(custom);
        // Compact numeric diagnostic every update.
        try {
          const nums = collectPositiveNumberFields(custom);
          const keys = Object.keys(nums).slice(0, 40).map((k) => `${k}=${nums[k]}`).join(',');
          if (keys) {
            console.error(`[nim-bridge] LIVEUPDATE room=${binding.pocketRoomId} pick=${onlineNum ?? '-'} fields=${keys}`);
          }
        } catch {}
        // Full raw dump (rate-limited) for finding fluctuating concurrent fields next live.
        dumpLiveUpdateRaw(binding, custom, raw);
        if (onlineNum !== undefined) emit('live_update', { ...base, data: { onlineNum, time } });
      }

      if (messageType === 'CLOSELIVE') {
        // Always dump once on close if we have custom payload.
        try {
          liveUpdateDumpLastAt.delete(String(binding?.pocketRoomId || binding?.nimRoomId || ''));
          dumpLiveUpdateRaw(binding, custom, raw);
        } catch {}
        emit('live_ended', { ...base, data: { onlineNum: liveOnlineCount(custom), time, reason: 'CLOSELIVE' } });
        void disconnectLive(binding.nimRoomId, 'live closed');
      }
      continue;
    }

    if (type === 5 || type === 'notification') {
      const attach = parseJSON(firstValue(raw.msg_attach_, raw.attach, raw.custom, raw.msg_setting_?.ext_)) || {};
      const notificationId = Number(firstValue(attach.id_, attach.id, attach.notificationId, 0));
      const event = String(firstValue(attach.event, attach.type, attach.notificationType, '') || '');
      const normalizedEvent = notificationId === 301 || /enter|memberin/i.test(event)
        ? 'memberEnter'
        : (notificationId === 302 || /exit|leave/i.test(event) ? 'memberExit' : '');
      if (!normalizedEvent) continue;
      let members = attach.members || attach.memberList;
      if (!Array.isArray(members)) {
        const ids = attach.target_ids_ || attach.targetIds || [];
        const nicks = attach.target_nick_ || attach.targetNicks || [];
        members = ids.map((account, index) => ({ account, nick: nicks[index] || '' }));
      }
      if (!members.length) {
        members = [attach.member || attach.user || custom.user || {
          account: firstValue(attach.operator_id_, attach.operatorId, user.from),
          nick: firstValue(attach.operator_nick_, attach.operatorNick, user.nick)
        }];
      }
      for (const member of members) {
        emit('member_event', {
          ...base,
          data: {
            event: normalizedEvent,
            userId: String(firstValue(member.account, member.userId, member.id, user.from, '')),
            nick: String(firstValue(member.nick, member.nickName, member.nickname, user.nick, '')),
            time
          }
        });
      }
    }
  }
}

async function connectAndroidLive(command, binding) {
  const account = String(command.account || '');
  const token = String(command.token || '');
  if (!account || !token) throw new Error('Android chatroom requires NIM account and token');

  const client = new AndroidChatroomClient({
    appKey: APP_KEY,
    account,
    token,
    roomId: binding.nimRoomId,
    onMessage(message) { handleLiveMessages(binding, [message]); },
    onConnected() {
      emit('live_connected', { roomId: binding.pocketRoomId, nimRoomId: binding.nimRoomId, mode: 'android' });
    },
    onDisconnected() {
      emit('live_disconnected', {
        roomId: binding.pocketRoomId,
        nimRoomId: binding.nimRoomId,
        msg: 'Android chatroom reconnecting'
      });
    },
    onError(error) {
      log(`Android chatroom ${binding.nimRoomId}: ${errorMessage(error)}`);
    }
  });
  liveRooms.set(binding.nimRoomId, {
    kind: 'android', client, binding,
    disconnect() { client.disconnect(); }
  });
  try {
    await client.connect();
    log(`Android chatroom connected room=${binding.nimRoomId}`);
  } catch (error) {
    liveRooms.delete(binding.nimRoomId);
    client.disconnect();
    throw error;
  }
}

async function connectLive(command) {
  const nimRoomId = Number(command.nimRoomId);
  const pocketRoomId = Number(command.roomId);
  if (!nimRoomId || !pocketRoomId) throw new Error('connect_live requires roomId and nimRoomId');

  const existing = liveRooms.get(nimRoomId);
  if (existing) {
    existing.binding.pocketRoomId = pocketRoomId;
    existing.binding.liveId = command.liveId || '';
    emit('live_connected', { roomId: pocketRoomId, nimRoomId });
    return;
  }

  const binding = { pocketRoomId, nimRoomId, liveId: command.liveId || '' };
  await connectAndroidLive(command, binding);
}

async function disconnectLive(nimRoomId, reason = 'requested') {
  const current = liveRooms.get(Number(nimRoomId));
  if (!current) return;
  liveRooms.delete(Number(nimRoomId));
  try {
    current.disconnect?.();
  } catch (error) {
    log(`disconnect live ${nimRoomId}: ${errorMessage(error)}`);
  }
  emit('live_disconnected', { roomId: current.binding.pocketRoomId, nimRoomId: Number(nimRoomId), msg: reason });
}

function normalizeQChatMessage(raw) {
  const type = String(firstValue(raw.type, raw.messageType, raw.msgType, '') || '').toLowerCase();
  let attach = firstValue(raw.attach, raw.attachment);
  if (attach === undefined && type === 'custom') attach = parseJSON(raw.body) || {};
  return {
    serverId: Number(firstValue(raw.serverId, raw.server_id_, 0)),
    channelId: Number(firstValue(raw.channelId, raw.channel_id_, 0)),
    from: String(firstValue(raw.from, raw.fromAccount, '') || ''),
    fromNick: String(firstValue(raw.fromNick, raw.fromNickname, '') || ''),
    type,
    body: typeof raw.body === 'string' ? raw.body : '',
    attach: parseJSON(attach) || attach || undefined,
    ext: parseJSON(raw.ext) || raw.ext || undefined,
    time: Number(firstValue(raw.time, raw.createTime, raw.timetag, Date.now())),
    idServer: String(firstValue(raw.idServer, raw.msgIdServer, raw.messageId, '')),
    idClient: String(firstValue(raw.idClient, raw.msgIdClient, raw.clientId, ''))
  };
}

function handleQChatMessages(event) {
  const messages = Array.isArray(event) ? event : (event?.messages || [event]);
  for (const raw of messages) {
    const data = normalizeQChatMessage(raw);
    const binding = qchatState.rooms.get(data.channelId);
    if (!binding || (binding.serverId && data.serverId && binding.serverId !== data.serverId)) continue;
    emit('room_message', { roomId: binding.pocketRoomId, channelId: data.channelId, data });
  }
}

async function destroyQChat() {
  qchatState.connected = false;
  try { qchatState.client?.disconnect(); } catch {}
  qchatState.client = undefined;
  qchatState.credentialKey = '';
}

async function ensureQChat(account, token) {
  const credentialKey = `${account}\0${token}`;
  const serverIds = [...new Set([...qchatState.rooms.values()].map((room) => Number(room.serverId)).filter(Boolean))];
  if (qchatState.client && qchatState.credentialKey === credentialKey) {
    qchatState.client.updateSubscriptions(serverIds);
    return;
  }
  await destroyQChat();
  qchatState.credentialKey = credentialKey;
  qchatState.client = new AndroidQChatClient({
    appKey: APP_KEY,
    account,
    token,
    onMessage: handleQChatMessages,
    onConnected() {
      qchatState.connected = true;
      emit('qchat_connected');
    },
    onDisconnected() {
      qchatState.connected = false;
      emit('qchat_disconnected', { msg: 'QChat reconnecting' });
    },
    onError(error) {
      emit('error', { msg: `QChat: ${errorMessage(error)}`, code: Number(error?.code || 0) });
    }
  });
  await qchatState.client.connect(serverIds);
}

async function syncRooms(command) {
  if (!command.account || !command.token) throw new Error('sync_rooms requires account and token');
  qchatState.rooms = new Map((command.rooms || []).map((room) => [Number(room.channelId), {
    serverId: Number(room.serverId),
    pocketRoomId: Number(room.pocketRoomId)
  }]));
  await ensureQChat(command.account, command.token);
}

async function shutdown() {
  clearInterval(healthTimer);
  for (const nimRoomId of [...liveRooms.keys()]) await disconnectLive(nimRoomId, 'shutdown');
  await destroyQChat();
  for (const peer of peers) peer.close();
  await new Promise((resolve) => server.close(resolve));
}

async function handleCommand(command) {
  switch (command.cmd) {
    case 'connect':
    case 'connect_live':
      await connectLive({ ...command, nimRoomId: command.nimRoomId || command.roomId });
      break;
    case 'disconnect_live':
      await disconnectLive(command.nimRoomId);
      break;
    case 'sync_rooms':
      await syncRooms(command);
      break;
    case 'shutdown':
      await shutdown();
      process.exit(0);
      break;
    default:
      throw new Error(`unknown command: ${command.cmd}`);
  }
}

wss.on('connection', (peer) => {
  peers.add(peer);
  peer.on('close', () => peers.delete(peer));
  peer.on('message', (data) => {
    let command;
    try {
      command = JSON.parse(data.toString());
    } catch (error) {
      emit('error', { msg: `invalid command JSON: ${errorMessage(error)}` });
      return;
    }
    void handleCommand(command).catch((error) => {
      emit('error', { roomId: Number(command.roomId || 0), nimRoomId: Number(command.nimRoomId || 0), msg: errorMessage(error), code: Number(error?.code || 0) });
    });
  });
});

server.listen({ host: '127.0.0.1', port: requestedPort }, () => {
  const address = server.address();
  process.stdout.write(`PORT:${address.port}\n`);
  healthTimer = setInterval(() => {
    const liveConnected = [...liveRooms.values()].filter((item) => item.client?.connected).length;
    emit('nim_status', {
      qchatConnected: qchatState.connected,
      liveConnected,
      liveConfigured: liveRooms.size,
    });
  }, 30_000);
  healthTimer.unref?.();
});

process.on('SIGINT', () => void shutdown().finally(() => process.exit(0)));
process.on('SIGTERM', () => void shutdown().finally(() => process.exit(0)));
process.on('uncaughtException', (error) => emit('error', { msg: `uncaught: ${errorMessage(error)}` }));
process.on('unhandledRejection', (error) => emit('error', { msg: `unhandled: ${errorMessage(error)}` }));
