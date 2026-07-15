import crypto from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';
import process from 'node:process';
import zlib from 'node:zlib';

const APP_KEY = '632feff1f4c838541ab75195d1ceb3fa';
const PACKAGE_NAME = 'com.seine48.app';
const SDK_VERSION = 92110;
const SDK_HUMAN_VERSION = '9.21.10';
const USER_AGENT = 'Native/9.21.10.14184';
const DEFAULT_LINK_HOST = 'link.netease.im';
const DEFAULT_LINK_PORT = 8080;
const RSA_KEY_VERSION = 0;
const RSA_SPKI_BASE64 = 'MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCBxLuL8+xpQSddSnSvPkvNOHdcr5Euqw+kkOSzO/buDMheCfFILRC/v5+nv8BsL7/YZWVpDA8sIBTxfNRqSCu0uLjlbJqT/sMnPT1xxdQrkb1HSnuSyTbZbqaInQ13tBE2SfcAhsQZJJ1hKQSE2QyKOMxQPhP583qcsIhDbdExvwIDAQAB';

const [configPath, target = DEFAULT_LINK_HOST, explicitPort = String(DEFAULT_LINK_PORT)] = process.argv.slice(2);
if (!configPath) {
  process.stderr.write('usage: node probe-nim-protocol.mjs <private-config.json> [host|chatroom:<roomId>] [port]\n');
  process.exit(2);
}

const privateConfig = JSON.parse(fs.readFileSync(configPath, 'utf8'));
const account = String(privateConfig.NIM_ACCOUNT || '');
const token = String(privateConfig.NIM_TOKEN || '');
if (!account || !token) {
  process.stderr.write('private config does not contain NIM_ACCOUNT/NIM_TOKEN\n');
  process.exit(2);
}

const roomId = target.startsWith('chatroom:') ? target.slice('chatroom:'.length) : '';
const listenMs = Math.max(0, Number(process.env.NIM_PROBE_LISTEN_MS || 0));

async function resolveTarget() {
  if (!roomId) return { host: target, port: Number(explicitPort) };
  const query = new URLSearchParams({
    k: APP_KEY,
    id: account,
    rid: roomId,
    v: String(SDK_VERSION),
    tp: '1',
    dt: '0',
    nt: '2'
  });
  const response = await fetch(`https://lbs.netease.im/lbs/chat.jsp?${query}`, {
    signal: AbortSignal.timeout(15000)
  });
  if (!response.ok) throw new Error(`chatroom LBS HTTP ${response.status}`);
  const result = await response.json();
  const address = Array.isArray(result.link) ? result.link[0] : undefined;
  if (!address) throw new Error('chatroom LBS returned no TCP link');
  const separator = address.lastIndexOf(':');
  if (separator <= 0) throw new Error('chatroom LBS returned invalid TCP link');
  return { host: address.slice(0, separator), port: Number(address.slice(separator + 1)) };
}

function encodeVarint(value) {
  const bytes = [];
  let remaining = value >>> 0;
  do {
    let byte = remaining % 128;
    remaining = Math.floor(remaining / 128);
    if (remaining > 0) byte |= 0x80;
    bytes.push(byte);
  } while (remaining > 0);
  return Buffer.from(bytes);
}

function decodeVarint(buffer, offset = 0) {
  let value = 0;
  let multiplier = 1;
  for (let index = offset; index < buffer.length && index < offset + 5; index += 1) {
    const byte = buffer[index];
    value += (byte & 0x7f) * multiplier;
    if ((byte & 0x80) === 0) return { value, bytes: index - offset + 1 };
    multiplier *= 128;
  }
  return null;
}

function lengthPrefixed(value) {
  const data = Buffer.isBuffer(value) ? value : Buffer.from(String(value), 'utf8');
  return Buffer.concat([encodeVarint(data.length), data]);
}

function int32LE(value) {
  const result = Buffer.allocUnsafe(4);
  result.writeInt32LE(value);
  return result;
}

function packet(sid, cid, serial, body, tag = 0) {
  const fixedHeader = Buffer.allocUnsafe(5);
  fixedHeader[0] = sid;
  fixedHeader[1] = cid;
  fixedHeader.writeInt16LE(serial, 2);
  fixedHeader[4] = tag;
  const size = fixedHeader.length + body.length;
  return Buffer.concat([encodeVarint(size), fixedHeader, body]);
}

function propertyMap(properties) {
  const entries = [...properties.entries()]
    .filter(([, value]) => value !== undefined && value !== null && value !== '')
    .sort(([left], [right]) => left - right);
  const parts = [encodeVarint(entries.length)];
  for (const [key, value] of entries) {
    parts.push(encodeVarint(key), lengthPrefixed(String(value)));
  }
  return Buffer.concat(parts);
}

class RC4 {
  constructor(key) {
    this.s = Uint8Array.from({ length: 256 }, (_, index) => index);
    this.i = 0;
    this.j = 0;
    let j = 0;
    for (let i = 0; i < 256; i += 1) {
      j = (j + this.s[i] + key[i % key.length]) & 0xff;
      [this.s[i], this.s[j]] = [this.s[j], this.s[i]];
    }
  }

  transform(input) {
    const output = Buffer.allocUnsafe(input.length);
    for (let offset = 0; offset < input.length; offset += 1) {
      this.i = (this.i + 1) & 0xff;
      this.j = (this.j + this.s[this.i]) & 0xff;
      [this.s[this.i], this.s[this.j]] = [this.s[this.j], this.s[this.i]];
      const keyByte = this.s[(this.s[this.i] + this.s[this.j]) & 0xff];
      output[offset] = input[offset] ^ keyByte;
    }
    return output;
  }
}

function rsaEncryptChunks(publicKey, plaintext) {
  const encrypted = [];
  for (let offset = 0; offset < plaintext.length; offset += 117) {
    encrypted.push(crypto.publicEncrypt({
      key: publicKey,
      padding: crypto.constants.RSA_PKCS1_PADDING
    }, plaintext.subarray(offset, offset + 117)));
  }
  return Buffer.concat(encrypted);
}

function parsePacket(raw) {
  const length = decodeVarint(raw);
  if (!length) throw new Error('invalid packet length');
  let offset = length.bytes;
  const sid = raw[offset];
  const cid = raw[offset + 1];
  const serial = raw.readInt16LE(offset + 2);
  const tag = raw[offset + 4];
  offset += 5;
  let resultCode = 200;
  if ((tag & 2) !== 0) {
    resultCode = raw.readInt16LE(offset);
    offset += 2;
  }
  let body = raw.subarray(offset);
  if ((tag & 1) !== 0) {
    if (body.length < 4) throw new Error('truncated compressed body');
    const expectedLength = body.readInt32LE(0);
    body = zlib.inflateSync(body.subarray(4));
    if (body.length !== expectedLength) throw new Error('compressed body length mismatch');
  }
  return { sid, cid, serial, tag, resultCode, body };
}

function parsePropertyMap(body) {
  let offset = 0;
  const count = decodeVarint(body, offset);
  if (!count) throw new Error('invalid property map count');
  offset += count.bytes;
  const result = new Map();
  for (let index = 0; index < count.value; index += 1) {
    const key = decodeVarint(body, offset);
    if (!key) throw new Error('invalid property key');
    offset += key.bytes;
    const length = decodeVarint(body, offset);
    if (!length) throw new Error('invalid property value length');
    offset += length.bytes;
    if (offset + length.value > body.length) throw new Error('truncated property value');
    result.set(key.value, body.subarray(offset, offset + length.value).toString('utf8'));
    offset += length.value;
  }
  return result;
}

const deviceId = crypto.randomUUID();
const sessionId = crypto.randomUUID();
const baseLoginProperties = new Map([
  [3, 1],
  [4, '8.0.0'],
  [6, SDK_VERSION],
  [8, 1],
  [9, 1],
  [13, deviceId],
  [16, 3],
  [18, APP_KEY],
  [19, account],
  [25, PACKAGE_NAME],
  [26, sessionId],
  [31, deviceId],
  [33, 0],
  [40, SDK_HUMAN_VERSION],
  [41, 1],
  [42, USER_AGENT],
  [114, 0],
  [115, 0],
  [1000, token]
]);
const baseLoginPacket = packet(2, 2, 1, propertyMap(baseLoginProperties));
const chatroomLoginTag = new Map([
  [1, APP_KEY],
  [2, account],
  [3, deviceId],
  [5, roomId],
  [8, 1]
]);
const chatroomAuthTag = new Map([
  [3, 1],
  [4, '8.0.0'],
  [6, SDK_VERSION],
  [9, 1],
  [13, deviceId],
  [18, APP_KEY],
  [19, account],
  [25, PACKAGE_NAME],
  [1000, token]
]);
const chatroomEnterBody = Buffer.concat([
  Buffer.from([2]),
  propertyMap(chatroomLoginTag),
  propertyMap(chatroomAuthTag)
]);
const applicationPacket = roomId
  ? packet(13, 2, 1, chatroomEnterBody)
  : baseLoginPacket;
const rc4Key = crypto.randomBytes(16);
const publicKey = crypto.createPublicKey({
  key: Buffer.from(RSA_SPKI_BASE64, 'base64'),
  format: 'der',
  type: 'spki'
});
const encryptedHandshakePayload = rsaEncryptChunks(
  publicKey,
  Buffer.concat([lengthPrefixed(rc4Key), applicationPacket])
);
const handshakePacket = packet(
  1,
  1,
  0,
  Buffer.concat([int32LE(RSA_KEY_VERSION), encryptedHandshakePayload])
);

const encryptor = new RC4(rc4Key);
const decryptor = new RC4(rc4Key);
let socket;
let decryptedBuffer = Buffer.alloc(0);
let loginSent = false;
let finished = false;
let listenTimer;
const received = { packets: 0, byCid: {}, messages: 0, byMsgType: {}, customKinds: {} };

function finish(result, exitCode = 0) {
  if (finished) return;
  finished = true;
  if (listenTimer) clearTimeout(listenTimer);
  process.stdout.write(`${JSON.stringify(result)}\n`);
  socket?.destroy();
  setTimeout(() => process.exit(exitCode), 25).unref();
}

function processPackets() {
  while (decryptedBuffer.length > 0) {
    const length = decodeVarint(decryptedBuffer);
    if (!length) return;
    const packetSize = length.bytes + length.value;
    if (decryptedBuffer.length < packetSize) return;
    const raw = decryptedBuffer.subarray(0, packetSize);
    decryptedBuffer = decryptedBuffer.subarray(packetSize);
    let response = parsePacket(raw);
    received.packets += 1;
    received.byCid[`${response.sid}/${response.cid}`] = (received.byCid[`${response.sid}/${response.cid}`] || 0) + 1;

    if (response.sid === 4 && [1, 2, 10, 11].includes(response.cid) && response.body.length > 8) {
      response = parsePacket(response.body.subarray(8));
      received.packets += 1;
      received.byCid[`${response.sid}/${response.cid}`] = (received.byCid[`${response.sid}/${response.cid}`] || 0) + 1;
    }

    if (response.sid === 1 && response.cid === 1) {
      if (response.resultCode !== 200) {
        finish({ stage: 'handshake', ...response, body: undefined }, 1);
        return;
      }
      if (!loginSent) {
        loginSent = true;
        socket.write(encryptor.transform(applicationPacket));
      }
      continue;
    }

    const expectedSid = roomId ? 13 : 2;
    if (response.sid === expectedSid && response.cid === 2) {
      if (!roomId || response.resultCode !== 200 || listenMs === 0) {
        finish({ stage: roomId ? 'chatroom-enter' : 'login', ...response, body: undefined }, response.resultCode === 200 ? 0 : 1);
        return;
      }
      listenTimer = setTimeout(() => finish({
        stage: 'chatroom-listen',
        enterResultCode: response.resultCode,
        listenMs,
        received
      }), listenMs);
      continue;
    }

    if (roomId && response.sid === 13 && response.cid === 7) {
      const properties = parsePropertyMap(response.body);
      const msgType = Number(properties.get(2) || -1);
      received.messages += 1;
      received.byMsgType[msgType] = (received.byMsgType[msgType] || 0) + 1;
      if (msgType === 100) {
        for (const field of [3, 4, 13]) {
          try {
            const custom = JSON.parse(properties.get(field) || '');
            const kind = String(custom.messageType || custom.type || (custom.giftInfo ? 'gift' : 'custom'));
            received.customKinds[kind] = (received.customKinds[kind] || 0) + 1;
            break;
          } catch {}
        }
      }
      continue;
    }
  }
}

let resolvedTarget;
try {
  resolvedTarget = await resolveTarget();
} catch (error) {
  process.stdout.write(`${JSON.stringify({ stage: 'resolve', error: error instanceof Error ? error.message : String(error) })}\n`);
  process.exit(1);
}

socket = net.createConnection(resolvedTarget);
socket.setTimeout(Math.max(20000, listenMs + 5000));
socket.on('connect', () => socket.write(handshakePacket));
socket.on('data', (chunk) => {
  decryptedBuffer = Buffer.concat([decryptedBuffer, decryptor.transform(chunk)]);
  try {
    processPackets();
  } catch (error) {
    finish({ stage: 'decode', error: error instanceof Error ? error.message : String(error) }, 1);
  }
});
socket.on('timeout', () => finish({ stage: loginSent ? (roomId ? 'chatroom-enter' : 'login') : 'handshake', timeout: true }, 1));
socket.on('error', (error) => finish({ stage: 'socket', error: error.message }, 1));
socket.on('close', () => {
  if (!finished) finish({ stage: loginSent ? (roomId ? 'chatroom-enter' : 'login') : 'handshake', closed: true }, 1);
});
