import { spawn } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';
import { WebSocket } from 'ws';

const profileDir = await fs.mkdtemp(path.join(os.tmpdir(), 'p48-weibo-auth-'));
const child = spawn(process.execPath, ['index.mjs', '--wsPort=0'], {
  cwd: import.meta.dirname,
  stdio: ['ignore', 'pipe', 'pipe'],
});
child.stderr.pipe(process.stderr);

let stdout = '';
const port = await new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('sidecar port timeout')), 10_000);
  child.stdout.on('data', (chunk) => {
    stdout += chunk.toString();
    const match = stdout.match(/PORT:(\d+)/);
    if (!match) return;
    clearTimeout(timer);
    resolve(Number(match[1]));
  });
  child.once('exit', (code) => reject(new Error(`sidecar exited early: ${code}`)));
});

const socket = new WebSocket(`ws://127.0.0.1:${port}/`);
await new Promise((resolve, reject) => {
  socket.once('open', resolve);
  socket.once('error', reject);
});

let result;
try {
  result = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('browser login probe timeout')), 90_000);
    socket.on('message', (raw) => {
      const event = JSON.parse(String(raw));
      if (event.type === 'log') return;
      process.stdout.write(`event=${event.type} status=${event.status || ''} message=${event.message || ''}\n`);
      if (event.type === 'qrcode' && event.imageBase64) {
        clearTimeout(timer);
        resolve('qrcode');
      } else if (event.type === 'cookies' && event.webCookie && event.mobileCookie) {
        clearTimeout(timer);
        resolve('cookies');
      } else if (event.type === 'error') {
        clearTimeout(timer);
        reject(new Error(event.message));
      }
    });
    socket.send(JSON.stringify({
      cmd: 'start',
      profileDir,
      headless: true,
      refreshMinutes: 30,
      webCookie: '',
      mobileCookie: '',
    }));
  });
} finally {
  if (socket.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({ cmd: 'shutdown' }));
  }
  await new Promise((resolve) => child.once('exit', resolve));
  await fs.rm(profileDir, { recursive: true, force: true });
}

process.stdout.write(`weibo browser probe passed via ${result}\n`);
