import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { spawn } from 'node:child_process';
import { WebSocket } from 'ws';

const [configPath, serverIdArg, channelIdArg] = process.argv.slice(2);
if (!configPath || !serverIdArg || !channelIdArg) {
  process.stderr.write('usage: node probe-sidecar.mjs <private-config.json> <serverId> <channelId>\n');
  process.exit(2);
}
const config = JSON.parse(fs.readFileSync(configPath, 'utf8'));
const sidecarPath = path.resolve(import.meta.dirname, '..', 'index.mjs');
const child = spawn(process.execPath, [sidecarPath, '--wsPort=0'], { stdio: ['ignore', 'pipe', 'pipe'] });
child.stderr.on('data', (data) => process.stderr.write(data));

const port = await new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('sidecar port timeout')), 10000);
  let buffer = '';
  child.stdout.on('data', (data) => {
    buffer += data.toString();
    const match = buffer.match(/PORT:(\d+)/);
    if (match) {
      clearTimeout(timer);
      resolve(Number(match[1]));
    }
  });
  child.once('exit', (code) => reject(new Error(`sidecar exited early: ${code}`)));
});

const socket = new WebSocket(`ws://127.0.0.1:${port}`);
await new Promise((resolve, reject) => {
  socket.once('open', resolve);
  socket.once('error', reject);
});
const result = await new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('qchat_connected event timeout')), 45000);
  socket.on('message', (raw) => {
    const event = JSON.parse(raw.toString());
    if (event.type === 'error') {
      clearTimeout(timer);
      reject(new Error(event.msg));
    } else if (event.type === 'qchat_connected') {
      clearTimeout(timer);
      resolve(event);
    }
  });
  socket.send(JSON.stringify({
    cmd: 'sync_rooms',
    account: config.NIM_ACCOUNT,
    token: config.NIM_TOKEN,
    rooms: [{
      serverId: Number(serverIdArg),
      channelId: Number(channelIdArg),
      pocketRoomId: Number(channelIdArg)
    }]
  }));
});
process.stdout.write(`${JSON.stringify({ connected: result.type === 'qchat_connected' })}\n`);
socket.send(JSON.stringify({ cmd: 'shutdown' }));
socket.close();
await new Promise((resolve) => child.once('exit', resolve));
