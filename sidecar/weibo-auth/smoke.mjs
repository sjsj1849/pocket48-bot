import { spawn } from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';
import { WebSocket } from 'ws';

const here = path.dirname(fileURLToPath(import.meta.url));
const secUserId = process.argv[2] || '';
const child = spawn(process.execPath, ['index.mjs', '--wsPort=0'], {
  cwd: here,
  stdio: ['ignore', 'pipe', 'pipe'],
});
child.stderr.pipe(process.stderr);

let buffer = '';
const port = await new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error('sidecar port timeout')), 10_000);
  child.stdout.on('data', (chunk) => {
    buffer += chunk.toString();
    const match = buffer.match(/PORT:(\d+)/);
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

if (secUserId) {
  socket.send(JSON.stringify({
    cmd: 'start',
    profileDir: path.join(os.tmpdir(), `pocket48-browser-smoke-${process.pid}`),
    headless: true,
    weiboEnabled: false,
    douyinEnabled: true,
    douyinPollSeconds: 300,
    douyinAccounts: [{ secUserId }],
  }));
  await new Promise((resolve, reject) => {
    let settleTimer;
    const timer = setTimeout(() => reject(new Error('Douyin scan timeout')), 90_000);
    const finish = () => {
      clearTimeout(timer);
      clearTimeout(settleTimer);
      resolve();
    };
    socket.on('message', (raw) => {
      const event = JSON.parse(String(raw));
      if (event.type === 'douyin_account_error' || event.type === 'douyin_error') {
        clearTimeout(timer);
        clearTimeout(settleTimer);
        reject(new Error(event.message));
      }
      if (event.type === 'douyin_account') {
        process.stdout.write(`douyin account: ${event.nickname || event.secUserId}, live=${event.liveId || '-'}\n`);
        settleTimer ||= setTimeout(finish, 5_000);
      }
      if (event.type === 'douyin_posts') {
        process.stdout.write(`douyin posts: ${event.nickname || event.secUserId} (${event.posts.length})\n`);
        finish();
      }
    });
  });
}

socket.send(JSON.stringify({ cmd: 'shutdown' }));

await new Promise((resolve, reject) => {
  const timer = setTimeout(() => {
    child.kill('SIGKILL');
    reject(new Error('sidecar shutdown timeout'));
  }, 10_000);
  child.once('exit', (code) => {
    clearTimeout(timer);
    if (code === 0) resolve();
    else reject(new Error(`sidecar exited with code ${code}`));
  });
});

process.stdout.write('unified browser sidecar smoke test passed\n');
