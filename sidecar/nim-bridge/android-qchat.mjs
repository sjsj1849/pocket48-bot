import crypto from 'node:crypto';
import net from 'node:net';
import zlib from 'node:zlib';

const PACKAGE_NAME = 'com.seine48.app';
const SDK_VERSION = 92110;
const SDK_HUMAN_VERSION = '9.21.10';
const USER_AGENT = 'Native/9.21.10.14184';
const RSA_KEY_VERSION = 0;
const RSA_SPKI_BASE64 = 'MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQCBxLuL8+xpQSddSnSvPkvNOHdcr5Euqw+kkOSzO/buDMheCfFILRC/v5+nv8BsL7/YZWVpDA8sIBTxfNRqSCu0uLjlbJqT/sMnPT1xxdQrkb1HSnuSyTbZbqaInQ13tBE2SfcAhsQZJJ1hKQSE2QyKOMxQPhP583qcsIhDbdExvwIDAQAB';
const PUBLIC_KEY = crypto.createPublicKey({
  key: Buffer.from(RSA_SPKI_BASE64, 'base64'), format: 'der', type: 'spki'
});

function encodeVarint(value) {
  const bytes = [];
  let remaining = Number(value);
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
  for (let index = offset; index < buffer.length && index < offset + 8; index += 1) {
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

function parsePropertyMap(body, offset = 0) {
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
  return { properties: result, offset };
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
      key: PUBLIC_KEY, padding: crypto.constants.RSA_PKCS1_PADDING
    }, plaintext.subarray(offset, offset + 117)));
  }
  return Buffer.concat(chunks);
}

function int32LE(value) {
  const result = Buffer.allocUnsafe(4);
  result.writeInt32LE(value);
  return result;
}

function int64LE(value) {
  const result = Buffer.allocUnsafe(8);
  result.writeBigInt64LE(BigInt(value));
  return result;
}

function applicationHandshake(applicationPacket) {
  const key = crypto.randomBytes(16);
  const encrypted = rsaEncrypt(Buffer.concat([lengthPrefixed(key), applicationPacket]));
  return {
    wire: packet(1, 1, 0, Buffer.concat([int32LE(RSA_KEY_VERSION), encrypted])),
    encryptor: new RC4(key), decryptor: new RC4(key)
  };
}

function baseLoginPacket(appKey, account, token, deviceId, serial) {
  return packet(2, 2, serial, propertyMap(new Map([
    [3, 1], [4, '8.0.0'], [6, SDK_VERSION], [8, 1], [9, 1],
    [13, deviceId], [16, 3], [18, appKey], [19, account],
    [25, PACKAGE_NAME], [26, crypto.randomUUID()], [31, deviceId],
    [33, 0], [40, SDK_HUMAN_VERSION], [41, 1], [42, USER_AGENT],
    [114, 0], [115, 0], [1000, token]
  ])));
}

function qchatLoginPacket(appKey, account, token, deviceId, serial) {
  return packet(24, 2, serial, propertyMap(new Map([
    [1, appKey], [2, account], [3, 0], [4, token], [6, 1],
    [7, '10'], [8, deviceId], [9, SDK_VERSION], [10, 1], [11, USER_AGENT],
    [14, SDK_HUMAN_VERSION], [30, '10'], [32, PACKAGE_NAME],
    [35, 1], [37, 'G8441']
  ])));
}

function parseAddress(raw) {
  const text = String(raw || '').trim();
  if (!text) throw new Error('empty QChat address');
  if (text.includes('://')) {
    const parsed = new URL(text);
    return { host: parsed.hostname, port: Number(parsed.port || 8080) };
  }
  const separator = text.lastIndexOf(':');
  if (separator <= 0) return { host: text, port: 8080 };
  return { host: text.slice(0, separator), port: Number(text.slice(separator + 1)) };
}

function parseStringList(body) {
  let offset = 0;
  const count = decodeVarint(body, offset);
  if (!count) throw new Error('invalid string list count');
  offset += count.bytes;
  const result = [];
  for (let index = 0; index < count.value; index += 1) {
    const length = decodeVarint(body, offset);
    if (!length) throw new Error('invalid string length');
    offset += length.bytes;
    result.push(body.subarray(offset, offset + length.value).toString('utf8'));
    offset += length.value;
  }
  return result;
}

function subscribeAllChannelsPacket(serverIds, serial) {
  const type = Buffer.allocUnsafe(4);
  type.writeInt32LE(1);
  return packet(25, 7, serial, Buffer.concat([
    type, encodeVarint(serverIds.length), ...serverIds.map(int64LE)
  ]));
}

function normalizeMessage(properties) {
  const numericType = Number(properties.get(9) || -1);
  return {
    serverId: Number(properties.get(1) || 0),
    channelId: Number(properties.get(2) || 0),
    from: properties.get(3) || '',
    fromNick: properties.get(6) || '',
    time: Number(properties.get(7) || Date.now()),
    type: numericType === 0 ? 'text' : ([100, 103].includes(numericType) ? 'custom' : String(numericType)),
    body: properties.get(10) || '',
    attach: properties.get(11) || '',
    ext: properties.get(12) || '',
    idClient: properties.get(13) || '',
    idServer: properties.get(14) || ''
  };
}

async function resolveQChatAddress({ appKey, account, token }) {
  const deviceId = crypto.randomUUID();
  const login = baseLoginPacket(appKey, account, token, deviceId, 1);
  const handshake = applicationHandshake(login);
  let buffer = Buffer.alloc(0);
  let loginSent = false;
  let tokenRequested = false;
  const seenPackets = [];
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host: 'link.netease.im', port: 8080 });
    const timer = setTimeout(() => {
      socket.destroy();
      reject(new Error(`QChat address request timeout (packets: ${seenPackets.join(',')})`));
    }, 20000);
    const fail = (error) => {
      clearTimeout(timer);
      socket.destroy();
      reject(error);
    };
    socket.on('connect', () => socket.write(handshake.wire));
    socket.on('data', (chunk) => {
      try {
        buffer = Buffer.concat([buffer, handshake.decryptor.transform(chunk)]);
        while (buffer.length) {
          const length = decodeVarint(buffer);
          if (!length || buffer.length < length.bytes + length.value) return;
          const size = length.bytes + length.value;
          let response = parsePacket(buffer.subarray(0, size));
          buffer = buffer.subarray(size);
          seenPackets.push(`${response.sid}/${response.cid}:${response.resultCode}:${response.body.length}`);
          if (response.sid === 4 && [1, 2, 10, 11].includes(response.cid) && response.body.length > 8) {
            response = parsePacket(response.body.subarray(8));
          }
          if (response.sid === 1 && response.cid === 1 && !loginSent) {
            if (response.resultCode !== 200) throw new Error(`IM handshake failed: ${response.resultCode}`);
            loginSent = true;
            socket.write(handshake.encryptor.transform(login));
          } else if (response.sid === 2 && response.cid === 2 && !tokenRequested) {
            if (response.resultCode !== 200) throw new Error(`IM login failed: ${response.resultCode}`);
            tokenRequested = true;
            socket.write(handshake.encryptor.transform(packet(24, 1, 2, propertyMap(new Map([[1, 0]])))));
          } else if (response.sid === 24 && [1, 2].includes(response.cid) && tokenRequested) {
            if (response.resultCode !== 200) throw new Error(`QChat address request failed: ${response.resultCode}`);
            const addresses = parseStringList(response.body);
            if (!addresses.length) throw new Error('QChat address response was empty');
            clearTimeout(timer);
            socket.destroy();
            resolve(parseAddress(addresses[0]));
          }
        }
      } catch (error) {
        fail(error);
      }
    });
    socket.on('error', fail);
    socket.on('close', () => {
      if (!tokenRequested) fail(new Error('IM connection closed before QChat address response'));
    });
  });
}

export class AndroidQChatClient {
  constructor({ appKey, account, token, onMessage, onConnected, onDisconnected, onError }) {
    Object.assign(this, { appKey, account, token, onMessage, onConnected, onDisconnected, onError });
    this.serial = 1;
    this.serverIds = [];
    this.closed = true;
    this.connected = false;
    this.reconnectAttempt = 0;
  }

  async connect(serverIds) {
    this.serverIds = [...new Set(serverIds.map(Number).filter(Boolean))];
    this.closed = false;
    if (this.connected) {
      this.#subscribe();
      return;
    }
    await this.#connectOnce();
  }

  updateSubscriptions(serverIds) {
    this.serverIds = [...new Set(serverIds.map(Number).filter(Boolean))];
    if (this.connected) this.#subscribe();
  }

  disconnect() {
    this.closed = true;
    this.connected = false;
    clearInterval(this.heartbeatTimer);
    clearTimeout(this.reconnectTimer);
    this.socket?.destroy();
  }

  #nextSerial() {
    const current = this.serial;
    this.serial = this.serial >= 32767 ? 2 : this.serial + 1;
    return current;
  }

  #write(applicationPacket) {
    if (this.socket && !this.socket.destroyed) this.socket.write(this.encryptor.transform(applicationPacket));
  }

  #subscribe() {
    if (this.serverIds.length) this.#write(subscribeAllChannelsPacket(this.serverIds, this.#nextSerial()));
  }

  async #connectOnce() {
    let address;
    let addressError;
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        address = await resolveQChatAddress(this);
        break;
      } catch (error) {
        addressError = error;
      }
    }
    if (!address) throw addressError || new Error('unable to resolve QChat address');
    const login = qchatLoginPacket(this.appKey, this.account, this.token, crypto.randomUUID(), this.#nextSerial());
    const handshake = applicationHandshake(login);
    this.encryptor = handshake.encryptor;
    this.decryptor = handshake.decryptor;
    this.buffer = Buffer.alloc(0);
    let loginSent = false;
    let loginAccepted = false;
    let settled = false;

    const markConnected = (timer, resolve) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      this.connected = true;
      this.reconnectAttempt = 0;
      this.#startHeartbeat();
      this.onConnected?.();
      resolve();
    };

    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.socket?.destroy();
        if (!settled) reject(new Error('QChat login timeout'));
      }, 20000);
      this.socket = net.createConnection(address);
      this.socket.setKeepAlive(true, 15000);
      this.socket.setTimeout(45000, () => {
        this.onError?.(new Error('QChat heartbeat timeout'));
        this.socket?.destroy();
      });
      this.socket.on('connect', () => this.socket.write(handshake.wire));
      this.socket.on('data', (chunk) => {
        try {
          this.buffer = Buffer.concat([this.buffer, this.decryptor.transform(chunk)]);
          while (this.buffer.length) {
            const length = decodeVarint(this.buffer);
            if (!length || this.buffer.length < length.bytes + length.value) return;
            const size = length.bytes + length.value;
            let response = parsePacket(this.buffer.subarray(0, size));
            this.buffer = this.buffer.subarray(size);
            if (response.sid === 4 && [1, 2, 10, 11].includes(response.cid) && response.body.length > 8) {
              response = parsePacket(response.body.subarray(8));
            }
            if (response.sid === 1 && response.cid === 1 && !loginSent) {
              if (response.resultCode !== 200) throw new Error(`QChat handshake failed: ${response.resultCode}`);
              loginSent = true;
              this.#write(login);
            } else if (response.sid === 24 && response.cid === 2) {
              if (response.resultCode !== 200) {
                let detail = response.body.toString('hex');
                try { detail = JSON.stringify(Object.fromEntries(parsePropertyMap(response.body).properties)); } catch {}
                throw new Error(`QChat login failed: ${response.resultCode} body=${detail}`);
              }
              if (loginAccepted) continue;
              loginAccepted = true;
              if (this.serverIds.length) this.#subscribe();
              else markConnected(timer, resolve);
            } else if (response.sid === 25 && response.cid === 7 && loginAccepted) {
              if (response.resultCode !== 200) throw new Error(`QChat subscription failed: ${response.resultCode}`);
              markConnected(timer, resolve);
            } else if (response.sid === 24 && response.cid === 11) {
              const { properties } = parsePropertyMap(response.body);
              this.onMessage?.(normalizeMessage(properties));
            }
          }
        } catch (error) {
          this.onError?.(error);
          this.socket?.destroy();
          if (!settled) reject(error);
        }
      });
      this.socket.on('error', (error) => {
        this.onError?.(error);
        if (!settled) reject(error);
      });
      this.socket.on('close', () => {
        clearTimeout(timer);
        const wasConnected = this.connected;
        this.connected = false;
        clearInterval(this.heartbeatTimer);
        if (!settled) reject(new Error('QChat connection closed during login'));
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
      if (this.connected) this.#write(packet(1, 2, this.#nextSerial(), Buffer.alloc(0)));
    }, 15000);
    this.heartbeatTimer.unref?.();
  }

  #scheduleReconnect() {
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
