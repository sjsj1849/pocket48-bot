import fs from 'node:fs';
import process from 'node:process';
import { AndroidQChatClient } from '../android-qchat.mjs';

/**
 * Dump raw QChat messages to see ALL message types and their format.
 * 
 * Usage:
 *   node probe-qchat-dump.mjs <config.json> [serverId=1] [listenSeconds=60]
 *
 * Outputs one JSON line per message to stdout.
 * Redirect stderr to see progress: node ... 2>/dev/null
 */
const [configPath, serverIdArg = '1', listenSecondsArg = '60'] = process.argv.slice(2);
if (!configPath) {
  process.stderr.write('usage: node probe-qchat-dump.mjs <config.json> [serverId] [listenSeconds]\n');
  process.exit(2);
}

const config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
const serverId = Number(serverIdArg);
const listenMs = Number(listenSecondsArg) * 1000;

const channels = new Set();
let count = 0;

const client = new AndroidQChatClient({
  appKey: '632feff1f4c838541ab75195d1ceb3fa',
  account: String(config.NIM_ACCOUNT || ''),
  token: String(config.NIM_TOKEN || ''),
  onConnected() { process.stderr.write(`[probe] QChat connected (serverId=${serverId})\n`); },
  onDisconnected() { process.stderr.write('[probe] QChat disconnected\n'); },
  onError(error) { process.stderr.write(`[probe] error: ${error?.message || error}\n`); },
  onMessage(message) {
    count++;
    channels.add(message.channelId);
    process.stdout.write(JSON.stringify({
      _seq: count,
      serverId: message.serverId,
      channelId: message.channelId,
      type: message.type,
      body: message.body,
      bodyLen: message.body?.length || 0,
      attach: message.attach,
      ext: message.ext,
      time: message.time,
      idServer: message.idServer,
      idClient: message.idClient,
    }) + '\n');
  }
});

try {
  process.stderr.write(`[probe] connecting to QChat server=${serverId} for ${listenSecondsArg}s...\n`);
  await client.connect([serverId]);
  process.stderr.write(`[probe] connected, listening...\n`);

  // Wait for the specified duration, or stop early if enough messages come in
  let elapsed = 0;
  const step = 1000;
  while (elapsed < listenMs) {
    await new Promise((r) => setTimeout(r, Math.min(step, listenMs - elapsed)));
    elapsed += step;
    if (count >= 20 && elapsed >= 10000) break; // 20 msgs or 10s minimum
  }

  process.stderr.write(`[probe] done: ${count} messages, ${channels.size} channel(s): [${[...channels].join(',')}]\n`);
  client.disconnect();
} catch (error) {
  process.stderr.write(`[probe] fatal: ${error?.message || error}\n`);
  client.disconnect();
  process.exitCode = 1;
}
