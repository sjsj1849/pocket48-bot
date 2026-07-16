import process from 'node:process';
import { WebSocket } from 'ws';

const endpoint = process.argv[2];
if (!endpoint) {
  process.stderr.write('usage: node live-smoke.mjs <ws://127.0.0.1:1088/ws/live_id>\n');
  process.exit(2);
}

const socket = new WebSocket(endpoint);
const result = await new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('douyinLive status timeout')), 60_000);
  socket.once('error', reject);
  socket.on('message', (raw) => {
    let event;
    try { event = JSON.parse(String(raw)); } catch { return; }
    if (event.type !== 'system' || event.event !== 'live_status') return;
    clearTimeout(timer);
    resolve(event);
  });
});
process.stdout.write(`${JSON.stringify(result)}\n`);
socket.close();
