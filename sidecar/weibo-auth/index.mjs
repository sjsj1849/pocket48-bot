import fs from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { chromium } from 'playwright';
import WebSocket, { WebSocketServer } from 'ws';
import { formatCookies, parseCookieHeader } from './cookies.mjs';
import { extractProfileLive, normalizeAwemeList } from './douyin-parser.mjs';
import { buildDouyinIMWebSocketURL, decodeDouyinIMInit, decodeDouyinIMPush } from './douyin-im.mjs';

const args = Object.fromEntries(process.argv.slice(2).map((arg) => {
  const [key, ...rest] = arg.replace(/^--/, '').split('=');
  return [key, rest.join('=')];
}));
const requestedPort = Number(args.wsPort || 0);

let context;
let browserStartPromise;
let page;
let douyinPage;
let douyinFollowPage;
let douyinIMPage;
let statePath;
let refreshTimer;
let douyinTimer;
let douyinSpecialFollowTimer;
let douyinIMReconnectTimer;
let douyinIMSocket;
let refreshRunning = false;
let douyinScanning = false;
let douyinSpecialFollowScanning = false;
let douyinIMStarting = false;
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
  weiboEnabled: true,
  douyinEnabled: false,
  douyinPollSeconds: 60,
  douyinAccounts: [],
  douyinSpecialFollowEnabled: false,
  douyinSpecialFollowMinutes: 30,
  douyinSpecialFollowIds: [],
  douyinIMEnabled: false,
  douyinIMPrivateEnabled: false,
  douyinIMGroupName: '',
  douyinIMGroupNumber: '',
};
let douyinIMIdentity = { selfUid: '', conversationId: '', ownerUid: '', groupName: '' };
const douyinIMSeen = new Set();
const douyinNicknameCache = new Map();

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

async function launchPersistentBrowser(profileDir, options) {
  try {
    return await chromium.launchPersistentContext(profileDir, options);
  } catch (error) {
    const message = String(error?.message || error);
    if (!message.includes('Executable doesn\'t exist') || !message.includes('headless_shell')) throw error;
    log('Chromium headless shell is not installed; falling back to the bundled full Chromium');
    return chromium.launchPersistentContext(profileDir, { ...options, channel: 'chromium' });
  }
}

async function startBrowser() {
  if (context) return;
  if (browserStartPromise) return browserStartPromise;
  browserStartPromise = (async () => {
    const profileDir = path.resolve(settings.profileDir);
    statePath = path.join(profileDir, 'weibo-storage-state.json');
    await fs.mkdir(profileDir, { recursive: true, mode: 0o700 });
    await fs.chmod(profileDir, 0o700).catch(() => {});

    const chromiumArgs = [
      '--disable-dev-shm-usage',
      '--autoplay-policy=user-gesture-required',
    ];
    if (typeof process.getuid === 'function' && process.getuid() === 0) {
      chromiumArgs.push('--no-sandbox');
    }
    context = await launchPersistentBrowser(profileDir, {
      headless: settings.headless,
      viewport: { width: 1280, height: 720 },
      args: chromiumArgs,
    });
    await context.addInitScript(() => {
      let mediaGestureUntil = 0;
      const allowMedia = () => { mediaGestureUntil = Date.now() + 1_500; };
      window.addEventListener('pointerdown', allowMedia, true);
      window.addEventListener('keydown', allowMedia, true);
      const nativePlay = HTMLMediaElement.prototype.play;
      HTMLMediaElement.prototype.play = function guardedPlay(...args) {
        if (Date.now() > mediaGestureUntil) {
          this.pause();
          return Promise.resolve();
        }
        return nativePlay.apply(this, args);
      };
    });
    await restoreStorageState();
    await seedConfiguredCookies();
    page = context.pages()[0] || await context.newPage();
    page.setDefaultTimeout(15_000);
    log(`browser profile ready at ${profileDir}`);
  })();
  try {
    await browserStartPromise;
  } finally {
    browserStartPromise = undefined;
  }
}

async function getDouyinPage() {
  await startBrowser();
  if (!douyinPage || douyinPage.isClosed()) {
    douyinPage = await context.newPage();
    douyinPage.setDefaultTimeout(15_000);
  }
  return douyinPage;
}

async function getDouyinFollowPage() {
  await startBrowser();
  if (!douyinFollowPage || douyinFollowPage.isClosed()) {
    douyinFollowPage = await context.newPage();
    douyinFollowPage.setDefaultTimeout(15_000);
  }
  return douyinFollowPage;
}

async function getDouyinIMPage() {
  await startBrowser();
  if (!douyinIMPage || douyinIMPage.isClosed()) {
    douyinIMPage = await context.newPage();
    douyinIMPage.setDefaultTimeout(15_000);
  }
  return douyinIMPage;
}

function stopDouyinIM() {
  clearTimeout(douyinIMReconnectTimer);
  douyinIMReconnectTimer = undefined;
  if (douyinIMSocket) {
    const socket = douyinIMSocket;
    douyinIMSocket = undefined;
    socket.removeAllListeners();
    socket.close();
  }
  douyinIMIdentity = { selfUid: '', conversationId: '', ownerUid: '', groupName: '' };
}

function scheduleDouyinIMReconnect() {
  clearTimeout(douyinIMReconnectTimer);
  if (shuttingDown || !settings.douyinIMEnabled) return;
  douyinIMReconnectTimer = setTimeout(() => void connectDouyinIM(), 5_000);
}

function rememberDouyinIMMessage(key) {
  if (!key || douyinIMSeen.has(key)) return false;
  douyinIMSeen.add(key);
  if (douyinIMSeen.size > 5_000) douyinIMSeen.delete(douyinIMSeen.values().next().value);
  return true;
}

async function resolveDouyinNickname(secUserId) {
  const sec = String(secUserId || '').trim();
  if (!sec) return '';
  if (douyinNicknameCache.has(sec)) return douyinNicknameCache.get(sec);
  try {
    const targetPage = await getDouyinIMPage();
    const nickname = await targetPage.evaluate(async (value) => {
      const response = await fetch(`/aweme/v1/web/user/profile/other/?sec_user_id=${encodeURIComponent(value)}`, { credentials: 'include' });
      if (!response.ok) return '';
      const body = await response.json();
      return String(body?.user?.nickname || body?.user_info?.nickname || body?.data?.user?.nickname || '');
    }, sec);
    douyinNicknameCache.set(sec, nickname);
    return nickname;
  } catch {
    return '';
  }
}

async function publishDouyinIMMessage(message) {
  if (!message?.conversationId || !message.senderUid) return;
  const isPrivate = message.conversationType === 1 && settings.douyinIMPrivateEnabled;
  const isTargetGroup = message.conversationType === 2
    && message.conversationId === douyinIMIdentity.conversationId;
  if (!isPrivate && !isTargetGroup) return;
  const key = message.serverMessageId || `${message.conversationId}:${message.index}`;
  if (!rememberDouyinIMMessage(key)) return;
  const senderName = await resolveDouyinNickname(message.senderSecUid);
  emit('douyin_im_message', { ...message, senderName });
}

async function connectDouyinIM() {
  if (shuttingDown || !settings.douyinIMEnabled || douyinIMStarting) return;
  if (!douyinIMIdentity.selfUid) return;
  if (douyinIMSocket && [WebSocket.CONNECTING, WebSocket.OPEN].includes(douyinIMSocket.readyState)) return;
  douyinIMStarting = true;
  try {
    const targetPage = await getDouyinIMPage();
    const cookies = await context.cookies(['https://www.douyin.com', 'https://frontier-im.douyin.com']);
    const socket = new WebSocket(
      buildDouyinIMWebSocketURL(douyinIMIdentity.selfUid),
      ['binary', 'base64', 'pbbp2'],
      {
        headers: {
          Cookie: cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join('; '),
          Origin: 'https://www.douyin.com',
          'User-Agent': await targetPage.evaluate(() => navigator.userAgent),
        },
      },
    );
    douyinIMSocket = socket;
    socket.on('open', () => emit('douyin_im_status', { status: 'connected', message: '抖音私信/群聊只读连接已建立' }));
    socket.on('message', (raw) => {
      try {
        const message = decodeDouyinIMPush(raw);
        if (message) void publishDouyinIMMessage(message);
      } catch (error) {
        log(`Douyin IM frame decode failed: ${error.message}`);
      }
    });
    socket.on('error', (error) => emit('douyin_im_status', { status: 'error', message: error.message }));
    socket.on('close', () => {
      if (douyinIMSocket === socket) douyinIMSocket = undefined;
      emit('douyin_im_status', { status: 'disconnected', message: '抖音私信/群聊只读连接已断开，准备重连' });
      scheduleDouyinIMReconnect();
    });
  } catch (error) {
    emit('douyin_im_status', { status: 'error', message: error.message });
    scheduleDouyinIMReconnect();
  } finally {
    douyinIMStarting = false;
  }
}

async function startDouyinIM() {
  if (!settings.douyinIMEnabled || shuttingDown || douyinIMStarting) return;
  const targetPage = await getDouyinIMPage();
  const targetGroupName = String(settings.douyinIMGroupName || '').trim();
  const targetGroupNumber = String(settings.douyinIMGroupNumber || '').trim();
  let initSeen = false;
  const onResponse = async (response) => {
    if (!response.url().includes('get_message_by_init')) return;
    initSeen = true;
    try {
      const decoded = decodeDouyinIMInit(await response.body());
      if (!decoded.selfUid || decoded.groups.length === 0) return;
      const nameMatches = targetGroupName
        ? decoded.groups.filter((item) => item.name === targetGroupName || item.name.includes(targetGroupName))
        : [];
      const group = decoded.groups.find((item) => targetGroupNumber && item.groupNumber === targetGroupNumber)
        || nameMatches.find((item) => item.name === targetGroupName)
        || (nameMatches.length === 1 ? nameMatches[0] : undefined);
      douyinIMIdentity = {
        selfUid: decoded.selfUid,
        conversationId: group?.conversationId || '',
        ownerUid: group?.ownerUid || '',
        groupName: group?.name || targetGroupName,
      };
      if (group) {
        emit('douyin_im_group', {
          groupName: group.name,
          groupNumber: group.groupNumber,
          conversationId: group.conversationId,
          ownerUid: group.ownerUid,
          selfUid: decoded.selfUid,
        });
      } else if (targetGroupNumber || targetGroupName) {
        const candidates = decoded.groups.map((item) => `${item.name || '未命名'}(${item.groupNumber || '-'})`).join('、');
        emit('douyin_im_status', {
          status: nameMatches.length > 1 ? 'group_ambiguous' : 'group_not_found',
          message: `未唯一匹配指定抖音群聊；当前群聊：${candidates || '无'}`,
        });
      }
      void connectDouyinIM();
    } catch (error) {
      log(`Douyin IM init decode failed: ${error.message}`);
    }
  };
  targetPage.on('response', onResponse);
  try {
    await targetPage.goto('https://www.douyin.com/follow', { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await targetPage.waitForTimeout(8_000);
    if (!initSeen) {
      emit('douyin_im_status', {
        status: 'init_missing',
        message: `抖音消息初始化接口未触发，当前页面：${targetPage.url()}`,
      });
    }
  } finally {
    targetPage.off('response', onResponse);
  }
}

async function scanDouyinSpecialFollows() {
  if (douyinSpecialFollowScanning || shuttingDown || !settings.douyinSpecialFollowEnabled) return;
  douyinSpecialFollowScanning = true;
  const accounts = new Map();
  const matchedIdentifiers = new Set();
  const targetPage = await getDouyinFollowPage();
  const identifiers = [...new Set((settings.douyinSpecialFollowIds || [])
    .map((item) => String(item || '').trim()).filter(Boolean))];
  const wanted = new Map(identifiers.map((item) => [item.toLowerCase(), item]));
  let followingResponses = 0;
  const onResponse = async (response) => {
    if (!response.url().includes('/aweme/v1/web/user/following/list/')) return;
    try {
      const body = await response.json();
      followingResponses += 1;
      for (const user of body?.followings || []) {
        const candidateIds = [user?.unique_id, user?.short_id, user?.uid]
          .map((item) => String(item || '').trim().toLowerCase()).filter(Boolean);
        const matched = candidateIds.find((item) => wanted.has(item));
        if (!matched || !user?.sec_uid) continue;
        matchedIdentifiers.add(wanted.get(matched));
        accounts.set(String(user.sec_uid), {
          secUserId: String(user.sec_uid),
          name: String(user.nickname || ''),
          profileUrl: `https://www.douyin.com/user/${encodeURIComponent(user.sec_uid)}`,
        });
      }
    } catch {}
  };
  targetPage.on('response', onResponse);
  try {
    if (identifiers.length === 0) throw new Error('未配置 DOUYIN_SPECIAL_FOLLOW_IDS');
    await targetPage.goto('https://www.douyin.com/user/self', { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await targetPage.waitForTimeout(3_000);
    await targetPage.locator('[data-e2e="user-info-follow"]').first().click({ timeout: 8_000 });
    let unchanged = 0;
    let previousResponses = -1;
    for (let index = 0; index < 30 && unchanged < 4 && matchedIdentifiers.size < identifiers.length; index += 1) {
      await targetPage.mouse.wheel(0, 8_000);
      await targetPage.waitForTimeout(1_000);
      if (followingResponses === previousResponses) unchanged += 1;
      else unchanged = 0;
      previousResponses = followingResponses;
    }
    if (followingResponses === 0) throw new Error('未收到关注列表接口响应');
    if (matchedIdentifiers.size !== identifiers.length || accounts.size !== identifiers.length) {
      throw new Error(`特别关注白名单只匹配到 ${matchedIdentifiers.size}/${identifiers.length} 个账号`);
    }
    emit('douyin_special_follows', { accounts: [...accounts.values()] });
  } catch (error) {
    emit('douyin_status', { status: 'special_follow_error', message: `特别关注同步失败: ${error.message}` });
  } finally {
    targetPage.off('response', onResponse);
    douyinSpecialFollowScanning = false;
  }
}

function scheduleDouyinSpecialFollows() {
  clearInterval(douyinSpecialFollowTimer);
  if (!settings.douyinSpecialFollowEnabled) return;
  const minutes = Math.max(10, Number(settings.douyinSpecialFollowMinutes) || 30);
  douyinSpecialFollowTimer = setInterval(() => void scanDouyinSpecialFollows(), minutes * 60_000);
  void scanDouyinSpecialFollows();
}

async function douyinPageSnapshot(targetPage, secUserId) {
  return targetPage.evaluate((fallbackSecUserId) => {
    const postSelector = 'a[href*="/video/"],a[href*="/note/"]';
    const roots = [
      document.querySelector('[data-e2e="user-post-list"]'),
      document.querySelector('[data-e2e="user-post-list-container"]'),
    ].filter(Boolean);
    const root = roots.find((candidate) => candidate.querySelector(postSelector)) || document;
    const cards = [];
    const seen = new Set();
    for (const anchor of root.querySelectorAll(postSelector)) {
      const match = anchor.href.match(/\/(video|note)\/(\d+)/);
      if (!match || seen.has(match[2])) continue;
      seen.add(match[2]);
      const image = anchor.querySelector('img');
      cards.push({
        id: match[2], secUserId: fallbackSecUserId, nickname: '',
        desc: anchor.getAttribute('aria-label') || anchor.getAttribute('title') || image?.alt || anchor.textContent?.trim() || '',
        createTime: 0, type: match[1], url: anchor.href,
        cover: image?.currentSrc || image?.src || '', images: [],
      });
    }
    const liveCandidates = [...document.querySelectorAll('a[href*="live.douyin.com/"]')]
      .map((anchor) => anchor.href)
      .filter((href) => /live\.douyin\.com\/\d+/.test(href));
    const live = liveCandidates.find((href) => href.includes('enter_method=web_homepage_head'))
      || (liveCandidates.length === 1 ? liveCandidates[0] : '');
    const liveMatch = live.match(/live\.douyin\.com\/(\d+)/);
    const liveActive = Boolean(document.querySelector('[data-e2e="user-info-living"]'));
    const nickname = document.querySelector('[data-e2e="user-title"], h1, h2')?.textContent?.trim() || '';
    return { cards, nickname, liveActive, liveId: liveMatch?.[1] || '' };
  }, secUserId);
}

async function scanDouyinAccount(account) {
  const secUserId = String(account.secUserId || '').trim();
  if (!secUserId) return;
  const targetPage = await getDouyinPage();
  const profileUrl = account.profileUrl || `https://www.douyin.com/user/${encodeURIComponent(secUserId)}`;
  let apiBody;
  let profileBody;
  const onResponse = async (response) => {
    try {
      const body = await response.json();
      if (response.url().includes('/aweme/v1/web/aweme/post/')) {
        if (Array.isArray(body?.aweme_list) && body.aweme_list.length > 0) apiBody = body;
      } else if (response.url().includes('/aweme/v1/web/user/profile/other/')) {
        profileBody = body;
      }
    } catch {}
  };
  targetPage.on('response', onResponse);
  try {
    await targetPage.goto(profileUrl, { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await targetPage.waitForTimeout(4_000);
    const snapshot = await douyinPageSnapshot(targetPage, secUserId);
    const posts = apiBody ? normalizeAwemeList(apiBody, secUserId) : snapshot.cards;
    const profileLive = extractProfileLive(profileBody);
    const nickname = profileLive.nickname || posts[0]?.nickname || snapshot.nickname || account.name || '';
    const discoveredLiveId = (profileLive.active || snapshot.liveActive) ? (profileLive.liveId || snapshot.liveId) : '';
    const liveId = discoveredLiveId || account.liveId || '';
    const finalProfileUrl = targetPage.url().includes('/user/') ? targetPage.url() : profileUrl;
    emit('douyin_account', { secUserId, profileUrl: finalProfileUrl, nickname, liveId });
    if (posts.length > 0) emit('douyin_posts', { secUserId, nickname, posts });
  } catch (error) {
    emit('douyin_account_error', { secUserId, message: error.message });
  } finally {
    targetPage.off('response', onResponse);
  }
}

async function scanAllDouyin() {
  if (douyinScanning || shuttingDown || !settings.douyinEnabled || settings.douyinAccounts.length === 0) return;
  douyinScanning = true;
  try {
    for (const account of settings.douyinAccounts) {
      if (shuttingDown) break;
      await scanDouyinAccount(account);
    }
  } finally {
    douyinScanning = false;
  }
}

function scheduleDouyin() {
  clearInterval(douyinTimer);
  if (!settings.douyinEnabled) return;
  const seconds = Math.max(15, Number(settings.douyinPollSeconds) || 60);
  douyinTimer = setInterval(() => void scanAllDouyin(), seconds * 1000);
}

async function requestDouyinLoginQRCode() {
  const targetPage = await getDouyinPage();
  await targetPage.goto('https://www.douyin.com/', { waitUntil: 'domcontentloaded', timeout: 45_000 });
  let cookies = new Map((await context.cookies()).map((cookie) => [cookie.name, cookie.value]));
  if (cookies.get('LOGIN_STATUS') === '1' || cookies.get('sessionid') || cookies.get('sessionid_ss')) {
    emit('douyin_status', { status: 'healthy', message: '抖音浏览器已登录' });
    return;
  }
  for (const selector of ['text=登录', '[data-e2e="login-button"]']) {
    try { await targetPage.locator(selector).first().click({ timeout: 3_000 }); break; } catch {}
  }
  for (const label of ['扫码登录', '扫码']) {
    try { await targetPage.getByText(label, { exact: true }).last().click({ timeout: 3_000 }); break; } catch {}
  }
  await targetPage.waitForTimeout(1_000);
  const selectors = [
    '#animate_qrcode_container img',
    '[class*="qrcode" i] img',
    '[class*="qr-code" i] img',
    '[class*="scan" i] img',
    'img[src*="qrcode"]',
    'img[src*="passport"]',
    'img[src^="data:image"]',
    'canvas',
  ];
  let image;
  const deadline = Date.now() + 15_000;
  while (!image && Date.now() < deadline) {
    for (const selector of selectors) {
      const candidates = targetPage.locator(selector);
      const count = Math.min(await candidates.count(), 10);
      for (let i = 0; i < count; i += 1) {
        const candidate = candidates.nth(i);
        try {
          if (!await candidate.isVisible()) continue;
          const box = await candidate.boundingBox();
          if (!box || box.width < 100 || box.height < 100) continue;
          image = await candidate.screenshot({ type: 'png' });
          break;
        } catch {}
      }
      if (image) break;
    }
    if (!image) await targetPage.waitForTimeout(500);
  }
  if (!image) {
    image = await targetPage.screenshot({ type: 'png' });
    emit('douyin_status', { status: 'qrcode_fallback', message: '未定位到独立二维码元素，已发送抖音登录页截图' });
  }
  emit('douyin_qrcode', { imageBase64: image.toString('base64'), expiresIn: 300 });
  const loginDeadline = Date.now() + 5 * 60_000;
  while (!shuttingDown && Date.now() < loginDeadline) {
    await targetPage.waitForTimeout(2_000);
    cookies = new Map((await context.cookies()).map((cookie) => [cookie.name, cookie.value]));
    if (cookies.get('LOGIN_STATUS') === '1' || cookies.get('sessionid') || cookies.get('sessionid_ss')) {
      await persistStorageState();
      emit('douyin_status', { status: 'healthy', message: '抖音浏览器登录成功' });
      void scanAllDouyin();
      void scanDouyinSpecialFollows();
      void startDouyinIM();
      return;
    }
  }
  emit('douyin_status', { status: 'qrcode_expired', message: '抖音登录二维码已过期' });
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
  // Primary: extract <img src> and fetch via Playwright API request.
  // This works even in headless shell where visibility checks fail.
  for (const selector of selectors) {
    const locator = page.locator(selector).first();
    if (await locator.count() === 0) continue;
    try {
      await locator.waitFor({ state: 'attached', timeout: 5_000 });
      const src = await locator.getAttribute('src', { timeout: 3_000 });
      if (src) {
        const response = await page.request.get(src);
        const buffer = await response.body();
        if (buffer && buffer.length > 100) return buffer;
      }
    } catch {
      // try next
    }
  }
  // Fallback: screenshot the visible img element.
  for (const selector of selectors) {
    const locator = page.locator(selector).first();
    if (await locator.count() === 0) continue;
    try {
      await locator.waitFor({ state: 'attached', timeout: 5_000 });
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
  clearInterval(douyinTimer);
  clearInterval(douyinSpecialFollowTimer);
  stopDouyinIM();
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
            weiboEnabled: command.weiboEnabled !== false,
            douyinEnabled: command.douyinEnabled === true,
            douyinPollSeconds: command.douyinPollSeconds || settings.douyinPollSeconds,
            douyinAccounts: Array.isArray(command.douyinAccounts) ? command.douyinAccounts : [],
            douyinSpecialFollowEnabled: command.douyinSpecialFollowEnabled === true,
            douyinSpecialFollowMinutes: command.douyinSpecialFollowMinutes || settings.douyinSpecialFollowMinutes,
            douyinSpecialFollowIds: Array.isArray(command.douyinSpecialFollowIds) ? command.douyinSpecialFollowIds : [],
            douyinIMEnabled: command.douyinIMEnabled === true,
            douyinIMPrivateEnabled: command.douyinIMPrivateEnabled === true,
            douyinIMGroupName: command.douyinIMGroupName || '',
            douyinIMGroupNumber: command.douyinIMGroupNumber || '',
          };
          if (settings.weiboEnabled) {
            scheduleRefresh();
            await refresh({ reason: 'startup' });
          } else {
            clearInterval(refreshTimer);
          }
          scheduleDouyin();
          scheduleDouyinSpecialFollows();
          if (settings.douyinIMEnabled) void startDouyinIM().catch((error) => {
            if (!shuttingDown) emit('douyin_im_status', { status: 'error', message: error.message });
          });
          else stopDouyinIM();
          emit('douyin_status', { status: 'ready', message: '抖音作品监控已就绪' });
          void scanAllDouyin();
          break;
        case 'refresh':
          await refresh({ allowQRCode: command.allowQRCode !== false, reason: command.reason || 'requested' });
          break;
        case 'douyin_sync':
          settings.douyinEnabled = true;
          settings.douyinAccounts = Array.isArray(command.douyinAccounts) ? command.douyinAccounts : [];
          scheduleDouyin();
          void scanAllDouyin();
          break;
        case 'douyin_scan':
          void scanAllDouyin();
          break;
        case 'douyin_login':
          try {
            await requestDouyinLoginQRCode();
          } catch (error) {
            emit('douyin_status', { status: 'login_error', message: error.message });
          }
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
