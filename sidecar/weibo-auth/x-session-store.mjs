import fs from 'node:fs/promises';
import path from 'node:path';
import { xSessionFromCookies } from './x-session.mjs';

// No network traffic or foreground tab changes. Never erase a saved session
// when the browser is logged out, and never broadcast cookie values.
export function createXSessionStore(context, dir, intervalMs = 60_000) {
  let queue = Promise.resolve(), timer, debounce, closed = false;
  const serialize = fn => { const next = queue.then(fn); queue = next.catch(() => {}); return next; };
  const fingerprint = session => JSON.stringify(session.cookies.map(c => [c.name, c.value, c.domain, c.expires]));
  async function read() {
    try {
      const file = path.join(dir, 'session.json');
      if ((await fs.stat(file)).mode & 0o077) return null;
      const saved = JSON.parse(await fs.readFile(file, 'utf8'));
      return xSessionFromCookies(saved.cookies || []);
    } catch { return null; }
  }
  async function syncNow() {
    const live = xSessionFromCookies(await context.cookies(['https://x.com/', 'https://twitter.com/']));
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
  const restore = (force = false) => serialize(async () => {
    const live = xSessionFromCookies(await context.cookies(['https://x.com/', 'https://twitter.com/']));
    if (live && !force) return { imported: false, reason: 'browser_session_exists' };
    const saved = await read();
    if (!saved) return { imported: false, reason: 'saved_session_missing' };
    await context.addCookies(saved.cookies.map(c => ({ ...c, path: '/', secure: true,
      httpOnly: c.name === 'auth_token', sameSite: c.name === 'auth_token' ? 'None' : 'Lax' })));
    return { imported: true, ...(await syncNow()) };
  });
  const onResponse = response => {
    let host; try { host = new URL(response.url()).hostname; } catch { return; }
    if (!['x.com', 'twitter.com'].includes(host) || closed) return;
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
