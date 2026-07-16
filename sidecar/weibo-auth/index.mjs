import fs from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { chromium } from 'playwright';
import { WebSocketServer } from 'ws';
import { formatCookies, parseCookieHeader } from './cookies.mjs';

const args = Object.fromEntries(process.argv.slice(2).map((arg) => {
  const [key, ...rest] = arg.replace(/^--/, '').split('=');
  return [key, rest.join('=')];
}));
const requestedPort = Number(args.wsPort || 0);

let context;
let page;
let statePath;
let refreshTimer;
let refreshRunning = false;
let qrRunning = false;
let shuttingDown = false;
let lastQRCodeAt = 0;
const qrCooldownMs = 2 * 60 * 60_000;
let settings = {
  profileDir: './storage/weibo-browser-profile',
  headless: true,
  refreshMinutes: 30,
  webCookie: '',
  mobileCookie: '',
};

const wss = new WebSocketServer({ host: '127.0.0.1', port: requestedPort });

function emit(type, payload = {}) {
  const message = JSON.stringify({ type, ...payload });
  for (const client of wss.clients) {
    if (client.readyState === 1) client.send(message);
  }
}

function log(message) {
  emit('log', { message });
  process.stderr.write(`[weibo-auth] ${message}\n`);
}

function cookieObjects(header, domain) {
  return [...parseCookieHeader(header).entries()].map(([name, value]) => ({
    name,
    value,
    domain,
    path: '/',
    secure: true,
    sameSite: 'Lax',
  }));
}

async function restoreStorageState() {
  try {
    const raw = await fs.readFile(statePath, 'utf8');
    const saved = JSON.parse(raw);
    if (Array.isArray(saved.cookies) && saved.cookies.length > 0) {
      await context.addCookies(saved.cookies);
      log(`restored ${saved.cookies.length} cookies from storage state`);
    }
  } catch (error) {
    if (error?.code !== 'ENOENT') log(`storage state restore failed: ${error.message}`);
  }
}

async function persistStorageState() {
  if (!context || !statePath) return;
  const tempPath = `${statePath}.tmp`;
  await context.storageState({ path: tempPath });
  await fs.chmod(tempPath, 0o600).catch(() => {});
  await fs.rename(tempPath, statePath);
}

async function seedConfiguredCookies() {
  const cookies = [
    ...cookieObjects(settings.webCookie, '.weibo.com'),
    ...cookieObjects(settings.mobileCookie || settings.webCookie, '.weibo.cn'),
  ];
  if (cookies.length > 0) {
    await context.addCookies(cookies);
    log(`seeded ${cookies.length} configured cookies`);
  }
}

async function startBrowser() {
  if (context) return;
  const profileDir = path.resolve(settings.profileDir);
  statePath = path.join(profileDir, 'weibo-storage-state.json');
  await fs.mkdir(profileDir, { recursive: true, mode: 0o700 });
  await fs.chmod(profileDir, 0o700).catch(() => {});

  const chromiumArgs = ['--disable-dev-shm-usage'];
  if (typeof process.getuid === 'function' && process.getuid() === 0) {
    chromiumArgs.push('--no-sandbox');
  }
  context = await chromium.launchPersistentContext(profileDir, {
    headless: settings.headless,
    channel: 'chromium',
    viewport: { width: 1280, height: 900 },
    args: chromiumArgs,
  });
  await restoreStorageState();
  await seedConfiguredCookies();
  page = context.pages()[0] || await context.newPage();
  page.setDefaultTimeout(15_000);
  log(`browser profile ready at ${profileDir}`);
}

async function mobileLoginState() {
  await page.goto('https://m.weibo.cn/', { waitUntil: 'domcontentloaded', timeout: 30_000 });
  await page.waitForTimeout(1_000);
  try {
    return await page.evaluate(async () => {
      const response = await fetch('/api/config', { credentials: 'include' });
      if (!response.ok) return false;
      const body = await response.json();
      return Boolean(body?.data?.login);
    });
  } catch {
    return false;
  }
}

async function preheatAndPublish(reason) {
  await page.goto('https://m.weibo.cn/', { waitUntil: 'domcontentloaded', timeout: 30_000 });
  await page.waitForTimeout(1_000);
  await page.goto('https://weibo.com/', { waitUntil: 'domcontentloaded', timeout: 30_000 });
  await page.waitForTimeout(1_000);

  const mobileCookies = await context.cookies(['https://m.weibo.cn/']);
  const webCookies = await context.cookies(['https://weibo.com/']);
  const webCookie = formatCookies(webCookies);
  const mobileCookie = formatCookies(mobileCookies);
  await persistStorageState();
  emit('cookies', { webCookie, mobileCookie, reason });
  log(`published refreshed cookies (${webCookies.length} web, ${mobileCookies.length} mobile)`);
}

async function findQRCode() {
  const selectors = [
    'img.w-full.h-full',
    'img[src*="qr.weibo.cn"]',
    'img[src*="qrcode"]',
  ];
  for (const selector of selectors) {
    const locator = page.locator(selector).first();
    if (await locator.count() === 0) continue;
    try {
      await locator.waitFor({ state: 'visible', timeout: 5_000 });
      return await locator.screenshot({ type: 'png' });
    } catch {
      // Try the next known selector.
    }
  }
  return null;
}

async function waitForLogin(previousSession) {
  const deadline = Date.now() + 10 * 60_000;
  while (!shuttingDown && Date.now() < deadline) {
    await page.waitForTimeout(2_000);
    const cookies = await context.cookies();
    const values = new Map(cookies.map((cookie) => [cookie.name, cookie.value]));
    if (values.get('SSOLoginState') || (values.get('WBPSESS') && values.get('WBPSESS') !== previousSession)) {
      return true;
    }
  }
  return false;
}

async function requestQRCode() {
  if (qrRunning || shuttingDown) return;
  if (Date.now() - lastQRCodeAt < qrCooldownMs) {
    emit('status', { status: 'qrcode_cooldown', message: '微博登录二维码仍在冷却期内' });
    return;
  }
  qrRunning = true;
  lastQRCodeAt = Date.now();
  try {
    const before = new Map((await context.cookies()).map((cookie) => [cookie.name, cookie.value]));
    await page.goto('https://passport.weibo.com/sso/signin?entry=miniblog&source=miniblog', {
      waitUntil: 'domcontentloaded',
      timeout: 30_000,
    });
    const image = await findQRCode();
    if (!image) throw new Error('未找到微博登录二维码');
    emit('qrcode', { imageBase64: image.toString('base64'), expiresIn: 600 });
    log('login QR code published; waiting for administrator scan');
    const loggedIn = await waitForLogin(before.get('WBPSESS'));
    if (!loggedIn) {
      emit('status', { status: 'qrcode_expired', message: '微博登录二维码已过期' });
      return;
    }
    await preheatAndPublish('login_restored');
    emit('status', { status: 'healthy', message: '微博浏览器登录态已恢复' });
  } catch (error) {
    if (!shuttingDown) emit('error', { message: `二维码登录失败: ${error.message}` });
  } finally {
    qrRunning = false;
  }
}

async function refresh({ allowQRCode = true, reason = 'scheduled' } = {}) {
  if (refreshRunning || qrRunning || shuttingDown) return;
  refreshRunning = true;
  try {
    await startBrowser();
    if (await mobileLoginState()) {
      await preheatAndPublish(reason);
      emit('status', { status: 'healthy', message: '微博浏览器登录态有效' });
    } else {
      emit('status', { status: 'login_required', message: '微博浏览器登录态已失效' });
      if (allowQRCode) void requestQRCode();
    }
  } catch (error) {
    emit('error', { message: `刷新微博登录态失败: ${error.message}` });
  } finally {
    refreshRunning = false;
  }
}

function scheduleRefresh() {
  clearInterval(refreshTimer);
  const minutes = Math.max(5, Number(settings.refreshMinutes) || 30);
  refreshTimer = setInterval(() => void refresh({ reason: 'scheduled' }), minutes * 60_000);
}

async function shutdown() {
  if (shuttingDown) return;
  shuttingDown = true;
  clearInterval(refreshTimer);
  try {
    await persistStorageState();
    if (context) await context.close();
  } finally {
    for (const client of wss.clients) client.terminate();
    wss.close(() => process.exit(0));
  }
}

wss.on('connection', (socket) => {
  socket.on('message', async (raw) => {
    try {
      const command = JSON.parse(String(raw));
      switch (command.cmd) {
        case 'start':
          settings = {
            ...settings,
            profileDir: command.profileDir || settings.profileDir,
            headless: command.headless !== false,
            refreshMinutes: command.refreshMinutes || settings.refreshMinutes,
            webCookie: command.webCookie || '',
            mobileCookie: command.mobileCookie || '',
          };
          scheduleRefresh();
          await refresh({ reason: 'startup' });
          break;
        case 'refresh':
          await refresh({ allowQRCode: command.allowQRCode !== false, reason: command.reason || 'requested' });
          break;
        case 'shutdown':
          await shutdown();
          break;
        default:
          emit('error', { message: `unknown command: ${command.cmd}` });
      }
    } catch (error) {
      emit('error', { message: error.message });
    }
  });
});

wss.on('listening', () => {
  const address = wss.address();
  process.stdout.write(`PORT:${address.port}\n`);
});

process.once('SIGINT', () => void shutdown());
process.once('SIGTERM', () => void shutdown());
