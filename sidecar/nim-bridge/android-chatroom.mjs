import crypto from 'node:crypto';
import net from 'node:net';
import zlib from 'node:zlib';

const PACKAGE_NAME = 'com.seine48.app';
const SDK_VERSION = 92110;
const RSA_KEY_VERSION = 0;
const RSA_SPKI_BASE64 = 'MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCBxLuL8+xpQSddSnSvPkvNOHdcr5Euqw+kkOSzO/buDMheCfFILRC/v5+nv8BsL7/YZWVpDA8sIBTxfNRqSCu0uLjlbJqT/sMnPT1xxdQrkb1HSnuSyTbZbqaInQ13tBE2SfcAhsQZJJ1hKQSE2QyKOMxQPhP583qcsIhDbdExvwIDAQAB';
const PUBLIC_KEY = crypto.createPublicKey({
  key: Buffer.from(RSA_SPKI_BASE64, 'base64'),
  format: 'der',
  type: 'spki'
});

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
  return undefined;
}

function lengthPrefixed(value) {
  const data = Buffer.isBuffer(value) ? value : Buffer.from(String(value), 'utf8');
  return Buffer.concat([encodeVarint(data.length), data]);
}

function propertyMap(properties) {
  const entries = [...properties.entries()]
    .filter(([, value]) => value !== undefined && value !== null && value !== '')
    .sort(([left], [right]) => left - right);
  const parts = [encodeVarint(entries.length)];
  for (const [key, value] of entries) parts.push(encodeVarint(key), lengthPrefixed(String(value)));
  return Buffer.concat(parts);
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

function packet(sid, cid, serial, body, tag = 0) {
  const header = Buffer.allocUnsafe(5);
  header[0] = sid;
  header[1] = cid;
  header.writeInt16LE(serial, 2);
  header[4] = tag;
  return Buffer.concat([encodeVarint(header.length + body.length), header, body]);
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
      output[offset] = input[offset] ^ this.s[(this.s[this.i] + this.s[this.j]) & 0xff];
    }
    return output;
  }
}

function rsaEncrypt(plaintext) {
  const chunks = [];
  for (let offset = 0; offset < plaintext.length; offset += 117) {
    chunks.push(crypto.publicEncrypt({
      key: PUBLIC_KEY,
      padding: crypto.constants.RSA_PKCS1_PADDING
    }, plaintext.subarray(offset, offset + 117)));
  }
  return Buffer.concat(chunks);
}

function int32LE(value) {
  const result = Buffer.allocUnsafe(4);
  result.writeInt32LE(value);
  return result;
}

function messageFromProperties(properties) {
  const type = Number(properties.get(2) || -1);
  const attach = properties.get(3) || '';
  return {
    uuid: properties.get(1) || '',
    msg_type_: type,
    msg_attach_: attach,
    msg_setting_: { ext_: properties.get(4) || '' },
    fromNick: properties.get(7) || '',
    fromAvatar: properties.get(8) || '',
    text: type === 0 ? attach : (properties.get(13) || ''),
    from: properties.get(21) || '',
    sessionId: properties.get(22) || '',
    fromClientType: Number(properties.get(23) || 0),
    time: Number(properties.get(20) || Date.now())
  };
}

export class AndroidChatroomClient {
  constructor({ appKey, account, token, roomId, onMessage, onConnected, onDisconnected, onError }) {
    this.appKey = String(appKey);
    this.account = String(account);
    this.token = String(token);
    this.roomId = String(roomId);
    this.onMessage = onMessage;
    this.onConnected = onConnected;
    this.onDisconnected = onDisconnected;
    this.onError = onError;
    this.closed = true;
    this.connected = false;
    this.reconnectAttempt = 0;
    this.serial = 1;
    this.seen = new Set();
  }

  async connect() {
    if (!this.account || !this.token || !this.roomId) throw new Error('Android chatroom requires account, token and roomId');
    this.closed = false;
    await this.#connectOnce();
  }

  disconnect() {
    this.closed = true;
    this.connected = false;
    clearInterval(this.heartbeatTimer);
    clearTimeout(this.reconnectTimer);
    this.socket?.destroy();
  }

  async #resolveAddress() {
    const query = new URLSearchParams({
      k: this.appKey,
      id: this.account,
      rid: this.roomId,
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

  #buildEnterPacket(deviceId) {
    const loginTag = propertyMap(new Map([
      [1, this.appKey],
      [2, this.account],
      [3, deviceId],
      [5, this.roomId],
      [8, 1]
    ]));
    const authTag = propertyMap(new Map([
      [3, 1],
      [4, '8.0.0'],
      [6, SDK_VERSION],
      [9, 1],
      [13, deviceId],
      [18, this.appKey],
      [19, this.account],
      [25, PACKAGE_NAME],
      [1000, this.token]
    ]));
    return packet(13, 2, this.#nextSerial(), Buffer.concat([Buffer.from([2]), loginTag, authTag]));
  }

  #nextSerial() {
    const current = this.serial;
    this.serial = this.serial >= 32767 ? 1 : this.serial + 1;
    return current;
  }

  async #connectOnce() {
    const address = await this.#resolveAddress();
    const rc4Key = crypto.randomBytes(16);
    const enterPacket = this.#buildEnterPacket(crypto.randomUUID());
    const handshakePayload = rsaEncrypt(Buffer.concat([lengthPrefixed(rc4Key), enterPacket]));
    const handshake = packet(1, 1, 0, Buffer.concat([int32LE(RSA_KEY_VERSION), handshakePayload]));
    this.encryptor = new RC4(rc4Key);
    this.decryptor = new RC4(rc4Key);
    this.decryptedBuffer = Buffer.alloc(0);
    let enterSent = false;
    let settled = false;

    await new Promise((resolve, reject) => {
      const rejectOnce = (error) => {
        if (settled) return;
        settled = true;
        reject(error);
      };
      const timer = setTimeout(() => {
        this.socket?.destroy();
        rejectOnce(new Error(`chatroom ${this.roomId} enter timeout`));
      }, 20000);

      this.socket = net.createConnection(address);
      this.socket.setKeepAlive(true, 15000);
      this.socket.setTimeout(45000, () => {
        this.onError?.(new Error(`chatroom ${this.roomId} heartbeat timeout`));
        this.socket?.destroy();
      });
      this.socket.on('connect', () => this.socket.write(handshake));
      this.socket.on('data', (chunk) => {
        try {
          this.decryptedBuffer = Buffer.concat([this.decryptedBuffer, this.decryptor.transform(chunk)]);
          while (this.decryptedBuffer.length > 0) {
            const length = decodeVarint(this.decryptedBuffer);
            if (!length) break;
            const size = length.bytes + length.value;
            if (this.decryptedBuffer.length < size) break;
            const raw = this.decryptedBuffer.subarray(0, size);
            this.decryptedBuffer = this.decryptedBuffer.subarray(size);
            let response = parsePacket(raw);

            if (response.sid === 4 && [1, 2, 10, 11].includes(response.cid) && response.body.length > 8) {
              response = parsePacket(response.body.subarray(8));
            }
            if (response.sid === 1 && response.cid === 1) {
              if (response.resultCode !== 200) throw new Error(`NIM handshake failed: ${response.resultCode}`);
              if (!enterSent) {
                enterSent = true;
                this.socket.write(this.encryptor.transform(enterPacket));
              }
              continue;
            }
            if (response.sid === 13 && response.cid === 2) {
              if (response.resultCode !== 200) throw new Error(`chatroom enter failed: ${response.resultCode}`);
              if (!settled) {
                settled = true;
                clearTimeout(timer);
                this.connected = true;
                this.reconnectAttempt = 0;
                this.#startHeartbeat();
                this.onConnected?.();
                resolve();
              }
              continue;
            }
            if (response.sid === 13 && response.cid === 7) {
              const properties = parsePropertyMap(response.body);
              const message = messageFromProperties(properties);
              if (message.uuid && this.seen.has(message.uuid)) continue;
              if (message.uuid) {
                this.seen.add(message.uuid);
                if (this.seen.size > 2048) this.seen.delete(this.seen.values().next().value);
              }
              this.onMessage?.(message);
              if (properties.get(38) === '1' && message.uuid) {
                const ackBody = propertyMap(new Map([[1, message.uuid], [2, this.roomId]]));
                const ack = packet(13, 35, this.#nextSerial(), ackBody);
                this.socket.write(this.encryptor.transform(ack));
              }
            }
          }
        } catch (error) {
          this.onError?.(error);
          this.socket?.destroy();
          rejectOnce(error);
        }
      });
      this.socket.on('error', (error) => {
        this.onError?.(error);
        rejectOnce(error);
      });
      this.socket.on('close', () => {
        clearTimeout(timer);
        const wasConnected = this.connected;
        this.connected = false;
        clearInterval(this.heartbeatTimer);
        if (!settled) rejectOnce(new Error(`chatroom ${this.roomId} connection closed`));
        if (wasConnected && !this.closed) {
          this.onDisconnected?.();
          this.#scheduleReconnect();
        }
      });
    });
  }

  #startHeartbeat() {
    clearInterval(this.heartbeatTimer);
    this.heartbeatTimer = setInterval(() => {
      if (!this.connected || !this.socket || this.socket.destroyed) return;
      const heartbeat = packet(1, 2, this.#nextSerial(), Buffer.alloc(0));
      this.socket.write(this.encryptor.transform(heartbeat));
    }, 15000);
    this.heartbeatTimer.unref?.();
  }

  #scheduleReconnect() {
    clearTimeout(this.reconnectTimer);
    const delay = Math.min(30000, 1000 * (2 ** Math.min(this.reconnectAttempt++, 5)));
    this.reconnectTimer = setTimeout(() => {
      if (this.closed) return;
      void this.#connectOnce().catch((error) => {
        this.onError?.(error);
        if (!this.closed) this.#scheduleReconnect();
      });
    }, delay);
    this.reconnectTimer.unref?.();
  }
}
