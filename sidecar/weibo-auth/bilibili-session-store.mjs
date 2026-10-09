import fs from 'node:fs/promises';
import path from 'node:path';
import { bilibiliSessionFromCookies } from './bilibili-session.mjs';

// 无网络请求、不抢前台标签页。浏览器掉登录时也绝不擦掉已保存的登录态，
// 并且永远不通过 WebSocket 广播 cookie 值。
const ORIGINS = ['https://www.bilibili.com/', 'https://bilibili.com/'];
const SYNC_HOSTS = new Set(['bilibili.com', 'www.bilibili.com', 'space.bilibili.com', 'passport.bilibili.com']);

export function createBilibiliSessionStore(context, dir, intervalMs = 60_000) {
  let queue = Promise.resolve(), timer, debounce, closed = false;
  const serialize = fn => { const next = queue.then(fn); queue = next.catch(() => {}); return next; };
  const fingerprint = session => JSON.stringify(session.cookies.map(c => [c.name, c.value, c.domain, c.expires]));

  async function read() {
    try {
      const file = path.join(dir, 'session.json');
      if ((await fs.stat(file)).mode & 0o077) return null;
      const saved = JSON.parse(await fs.readFile(file, 'utf8'));
      return bilibiliSessionFromCookies(saved.cookies || []);
    } catch { return null; }
  }

  async function syncNow() {
    const live = bilibiliSessionFromCookies(await context.cookies(ORIGINS));
    if (!live) return { synced: false, reason: 'browser_session_missing' };
    const saved = await read();
    if (saved && fingerprint(saved) === fingerprint(live)) return { synced: true, changed: false };
    await fs.mkdir(dir, { recursive: true, mode: 0o700 });
    await fs.chmod(dir, 0o700);
    const file = path.join(dir, 'session.json'), temp = `${file}.browser.tmp`;
    await fs.writeFile(temp, JSON.stringify({ ...live, updatedAt: Date.now() }), { mode: 0o600 });
    await fs.chmod(temp, 0o600);
    await fs.rename(temp, file);
    return { synced: true, changed: true };
  }

  const sync = () => serialize(syncNow);

  // 服务重启后把已保存的登录态注回浏览器，避免每次都要重新扫码。
  const restore = (force = false) => serialize(async () => {
    const live = bilibiliSessionFromCookies(await context.cookies(ORIGINS));
    if (live && !force) return { imported: false, reason: 'browser_session_exists' };
    const saved = await read();
    if (!saved) return { imported: false, reason: 'saved_session_missing' };
    await context.addCookies(saved.cookies.map(c => ({
      ...c,
      path: '/',
      secure: true,
      // SESSDATA 必须是 httpOnly + SameSite=None，否则 B站 侧不会认。
      httpOnly: c.name === 'SESSDATA',
      sameSite: 'None',
    })));
    return { imported: true, ...(await syncNow()) };
  });

  const onResponse = response => {
    let host; try { host = new URL(response.url()).hostname.toLowerCase(); } catch { return; }
    if (!SYNC_HOSTS.has(host) || closed) return;
    clearTimeout(debounce);
    debounce = setTimeout(() => { if (!closed) void sync().catch(() => {}); }, 1500);
    debounce.unref?.();
  };
  context.on('response', onResponse);
  timer = setInterval(() => { if (!closed) void sync().catch(() => {}); }, intervalMs);
  timer.unref?.();
  function stop() { closed = true; clearTimeout(debounce); clearInterval(timer); context.off('response', onResponse); }
  context.once('close', stop);

  return { sync, restore, stop };
}
