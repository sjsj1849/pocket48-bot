import { spawn } from 'node:child_process';
import path from 'node:path';
import process from 'node:process';
import WebSocket from 'ws';

const profileDir = process.env.DOUYIN_PROFILE_DIR || path.resolve('../../storage/weibo-browser-profile');
const groupNumber = String(process.env.DOUYIN_IM_GROUP_NUMBER || '').trim();
const groupName = String(process.env.DOUYIN_IM_GROUP_NAME || '').trim();
const specialFollowIds = String(process.env.DOUYIN_SPECIAL_FOLLOW_IDS || '')
  .split(',').map((item) => item.trim()).filter(Boolean);
if (!groupNumber) throw new Error('DOUYIN_IM_GROUP_NUMBER is required');
const child = spawn(process.execPath, ['index.mjs', '--wsPort=0'], {
  cwd: path.dirname(new URL(import.meta.url).pathname.replace(/^\/(.:)/, '$1')),
  stdio: ['ignore', 'pipe', 'inherit'],
});

let socket;
let groupReady = false;
let imReady = false;
let specialReady = false;
const timeout = setTimeout(() => finish(new Error('Douyin IM smoke test timed out')), 120_000);

function finish(error) {
  clearTimeout(timeout);
  if (socket?.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ cmd: 'shutdown' }));
  else child.kill();
  if (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

function maybeFinish() {
  if (!groupReady || !imReady || !specialReady) return;
  process.stdout.write('douyin IM read-only smoke test passed\n');
  finish();
}

child.stdout.setEncoding('utf8');
child.stdout.on('data', (chunk) => {
  for (const line of chunk.split(/\r?\n/)) {
    if (!line.startsWith('PORT:')) continue;
    socket = new WebSocket(`ws://127.0.0.1:${Number(line.slice(5))}`);
    socket.on('open', () => socket.send(JSON.stringify({
      cmd: 'start', profileDir, headless: true, weiboEnabled: false,
      douyinEnabled: true, douyinAccounts: [], douyinPollSeconds: 300,
      douyinSpecialFollowEnabled: true, douyinSpecialFollowMinutes: 30,
      douyinSpecialFollowIds: specialFollowIds,
      douyinIMEnabled: true, douyinIMPrivateEnabled: true,
      douyinIMGroupName: groupName, douyinIMGroupNumber: groupNumber,
    })));
    socket.on('message', (raw) => {
      const event = JSON.parse(String(raw));
      if (event.type === 'douyin_im_group') {
        if (event.groupNumber !== groupNumber) return finish(new Error('matched an unexpected Douyin group'));
        groupReady = true;
        process.stdout.write(`douyin group matched: ${event.groupName} (${event.groupNumber})\n`);
      } else if (event.type === 'douyin_im_status' && event.status === 'connected') {
        imReady = true;
        process.stdout.write('douyin IM websocket connected\n');
      } else if (event.type === 'douyin_special_follows') {
        if (event.accounts?.length !== specialFollowIds.length) {
          return finish(new Error(`expected ${specialFollowIds.length} configured Douyin accounts, got ${event.accounts?.length || 0}`));
        }
        specialReady = true;
        process.stdout.write(`douyin special follows: ${event.accounts?.length || 0}\n`);
      } else if (event.type === 'douyin_status' && event.status === 'special_follow_error') {
        return finish(new Error(event.message || 'Douyin special-follow sync failed'));
      }
      maybeFinish();
    });
  }
});
child.on('exit', (code) => {
  if (!groupReady || !imReady || !specialReady) finish(new Error(`sidecar exited before smoke test completed (${code})`));
});
