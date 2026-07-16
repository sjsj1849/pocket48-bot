import { spawn } from 'node:child_process';
import process from 'node:process';
import { WebSocket } from 'ws';

const child = spawn(process.execPath, ['index.mjs', '--wsPort=0'], {
  cwd: import.meta.dirname,
  stdio: ['ignore', 'pipe', 'pipe'],
});

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

process.stdout.write('weibo auth sidecar smoke test passed\n');
