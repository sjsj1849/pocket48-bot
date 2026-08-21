import fs from 'node:fs/promises';
import path from 'node:path';
import process from 'node:process';
import { chromium } from 'playwright';
import WebSocket, { WebSocketServer } from 'ws';
import { formatCookies, parseCookieHeader } from './cookies.mjs';
import { extractProfileLive, normalizeAwemeList } from './douyin-parser.mjs';
import { buildDouyinIMWebSocketURL, decodeDouyinIMInit, decodeDouyinIMPush, isOwnDouyinIMMessage, compactDouyinShareQuote, looksLikeDouyinShareCardText, shouldForwardOwnPrivate, rememberDouyinPrivatePeer } from './douyin-im.mjs';
import { extractXiaohongshuProfile, normalizeXiaohongshuNotes } from './xiaohongshu-parser.mjs';

const args = Object.fromEntries(process.argv.slice(2).map((arg) => {
  const [key, ...rest] = arg.replace(/^--/, '').split('=');
  return [key, rest.join('=')];
}));
const requestedPort = Number(args.wsPort || 0);

let context;
let browserStartPromise;
let page;
let douyinPage;
let douyinIMPage;
let douyinLookupPage;
let xiaohongshuPage;
let statePath;
let refreshTimer;
let douyinTimer;
let xiaohongshuTimer;
let douyinIMReconnectTimer;
let douyinIMInitRetryTimer;
let douyinIMHealthTimer;
let douyinContactSyncTimer;
let douyinContactPersistTimer;
let browserHousekeepTimer;
let douyinIMSocket;
let refreshRunning = false;
let douyinScanning = false;
let xiaohongshuScanning = false;
let douyinIMStarting = false;
let douyinIMInitRunning = false;
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
  douyinIMEnabled: false,
  douyinIMPrivateEnabled: false,
  douyinIMGroupName: '',
  douyinIMGroupNumber: '',
  xiaohongshuEnabled: false,
  // Default 5 minutes — avoid thrashing explore/login.
  xiaohongshuPollSeconds: 300,
  xiaohongshuAccounts: [],
  // Optional Playwright proxy, e.g. http://127.0.0.1:17890 (mihomo-xhs)
  proxyServer: '',
};
let douyinIMIdentity = { selfUid: '', conversationId: '', ownerUid: '', groupName: '', groupNumber: '' };
const douyinIMSeen = new Set();
const douyinNicknameCache = new Map();
const douyinContacts = new Map();
// Intentionally NO per-account page maps: one shared tab per platform scans
// accounts serially. Old 1-account-1-tab design left many profile tabs open
// forever and ballooned Chromium renderer memory on small VMs.
let profileScanRunning = false;
let profileScanQueued = null; // 'douyin' | 'xiaohongshu' | 'both' | null
let douyinContactSyncRunning = false;
const douyinContactSyncMs = 6 * 60 * 60_000;

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

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Poll until predicate is true or timeout. Returns true if predicate succeeded. */
async function waitUntil(predicate, { timeoutMs = 8_000, intervalMs = 200 } = {}) {
  const deadline = Date.now() + Math.max(0, timeoutMs);
  while (Date.now() < deadline) {
    try {
      if (await predicate()) return true;
    } catch {}
    const left = deadline - Date.now();
    if (left <= 0) break;
    await sleep(Math.min(intervalMs, left));
  }
  try {
    return Boolean(await predicate());
  } catch {
    return false;
  }
}

function douyinContactCachePath() {
  return path.resolve(settings.profileDir, 'douyin-contact-cache.json');
}

function douyinContactKey(contact) {
  return contact.uid ? `uid:${contact.uid}` : `sec:${contact.secUid}`;
}

function rememberDouyinContact(contact, { persist = false } = {}) {
  const hasMutual = typeof contact.mutual === 'boolean';
  // Empty remark from API must NOT wipe a previously saved remark.
  const rawRemark = Object.prototype.hasOwnProperty.call(contact, 'remarkName')
    ? String(contact.remarkName || '').trim()
    : null;
  const hasRemark = rawRemark !== null && rawRemark !== '';
  const normalized = {
    uid: String(contact.uid || '').trim(),
    secUid: String(contact.secUid || '').trim(),
    nickname: String(contact.nickname || '').trim(),
    remarkName: hasRemark ? rawRemark : '',
    mutual: contact.mutual === true,
    updatedAt: Number(contact.updatedAt) || Date.now(),
  };
  if ((!normalized.uid && !normalized.secUid) || !normalized.nickname) return false;
  const previous = (normalized.uid && douyinContacts.get(`uid:${normalized.uid}`))
    || (normalized.secUid && douyinContacts.get(`sec:${normalized.secUid}`));
  const merged = {
    ...previous,
    ...normalized,
    uid: normalized.uid || previous?.uid || '',
    secUid: normalized.secUid || previous?.secUid || '',
    // Keep previous remark unless a non-empty new one is provided.
    remarkName: hasRemark ? normalized.remarkName : (previous?.remarkName || ''),
    mutual: hasMutual ? normalized.mutual : previous?.mutual || false,
  };
  const displayName = merged.remarkName || merged.nickname;
  if (merged.uid) {
    douyinContacts.set(`uid:${merged.uid}`, merged);
    douyinNicknameCache.set(`uid:${merged.uid}`, displayName);
  }
  if (merged.secUid) {
    douyinContacts.set(`sec:${merged.secUid}`, merged);
    douyinNicknameCache.set(`sec:${merged.secUid}`, displayName);
  }
  if (persist) scheduleDouyinContactPersist();
  return true;
}

function cachedDouyinDisplayName(secUserId, userId) {
  const sec = String(secUserId || '').trim();
  const uid = String(userId || '').trim();
  return (sec && douyinNicknameCache.get(`sec:${sec}`))
    || (uid && douyinNicknameCache.get(`uid:${uid}`))
    || '';
}

function lookupDouyinContact(secUserId, userId) {
  const sec = String(secUserId || '').trim();
  const uid = String(userId || '').trim();
  return (uid && douyinContacts.get(`uid:${uid}`))
    || (sec && douyinContacts.get(`sec:${sec}`))
    || null;
}

/** Prefer contact cache (nickname + remark), fall back to single display name. */
function resolveDouyinSenderIdentity(secUserId, userId, displayHint = '') {
  const contact = lookupDouyinContact(secUserId, userId);
  const nickname = String(contact?.nickname || '').trim();
  const remarkName = String(contact?.remarkName || '').trim();
  const hint = String(displayHint || '').trim();
  // If only a combined display name is available, treat it as nickname when no cache hit.
  return {
    nickname: nickname || hint,
    remarkName,
  };
}

async function persistDouyinContacts() {
  clearTimeout(douyinContactPersistTimer);
  douyinContactPersistTimer = undefined;
  const unique = new Map();
  for (const contact of douyinContacts.values()) {
    unique.set(douyinContactKey(contact), contact);
  }
  const target = douyinContactCachePath();
  const temp = `${target}.tmp`;
  const payload = JSON.stringify({ version: 1, updatedAt: Date.now(), contacts: [...unique.values()] }, null, 2);
  await fs.mkdir(path.dirname(target), { recursive: true, mode: 0o700 });
  await fs.writeFile(temp, payload, { mode: 0o600 });
  await fs.rename(temp, target);
}

function scheduleDouyinContactPersist() {
  clearTimeout(douyinContactPersistTimer);
  douyinContactPersistTimer = setTimeout(() => {
    douyinContactPersistTimer = undefined;
    void persistDouyinContacts().catch((error) => log(`Douyin contact cache persist failed: ${error.message}`));
  }, 1_000);
}

async function loadDouyinContacts() {
  try {
    const saved = JSON.parse(await fs.readFile(douyinContactCachePath(), 'utf8'));
    for (const contact of saved.contacts || []) rememberDouyinContact(contact);
    const unique = new Set([...douyinContacts.values()].map(douyinContactKey));
    log(`loaded ${unique.size} persisted Douyin contacts`);
  } catch (error) {
    if (error?.code !== 'ENOENT') log(`Douyin contact cache load failed: ${error.message}`);
  }
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
      // Remember last good XHS session fingerprint for fail-safe persist.
      const xhs = saved.cookies.filter((c) => /xiaohongshu/i.test(String(c.domain || '')));
      const ws = xhs.find((c) => c.name === 'web_session')?.value || '';
      const a1 = xhs.find((c) => c.name === 'a1')?.value || '';
      if (ws && a1) {
        xiaohongshuLastGoodSession = {
          webSession: ws,
          a1,
          savedAt: Date.now(),
          source: 'restore',
        };
      }
    }
    // Restore xiaohongshu localStorage if we previously saved it (origins entry).
    xiaohongshuSavedLocalStorage = [];
    if (Array.isArray(saved.origins)) {
      for (const origin of saved.origins) {
        if (!/xiaohongshu\.com/i.test(String(origin?.origin || ''))) continue;
        if (Array.isArray(origin.localStorage) && origin.localStorage.length > 0) {
          xiaohongshuSavedLocalStorage = origin.localStorage
            .map((item) => ({ name: String(item.name || ''), value: String(item.value ?? '') }))
            .filter((item) => item.name);
          log(`restored xhs localStorage keys=${xiaohongshuSavedLocalStorage.length} from ${origin.origin}`);
        }
      }
    }
  } catch (error) {
    if (error?.code !== 'ENOENT') log(`storage state restore failed: ${error.message}`);
  }
}

/** Last known-good XHS session — never let a failed scan overwrite this with empty cookies. */
let xiaohongshuLastGoodSession = null;
/** localStorage snapshot for www/xiaohongshu (Playwright storageState only captures visited origins). */
let xiaohongshuSavedLocalStorage = [];
/** When true: login dead / need re-login — forbid navigate/refresh on xhs pages. */
let xiaohongshuLoginDead = false;
let xiaohongshuLoginDeadReason = '';

function markXiaohongshuLoginDead(reason = '') {
  xiaohongshuLoginDead = true;
  xiaohongshuLoginDeadReason = String(reason || 'login_required').slice(0, 200);
  log(`xiaohongshu login-dead ON: ${xiaohongshuLoginDeadReason}`);
}

function clearXiaohongshuLoginDead(reason = '') {
  if (xiaohongshuLoginDead) {
    log(`xiaohongshu login-dead OFF: ${reason || 'cleared'}`);
  }
  xiaohongshuLoginDead = false;
  xiaohongshuLoginDeadReason = '';
}

async function captureXiaohongshuLocalStorage(page) {
  if (!page || page.isClosed()) return [];
  try {
    const items = await page.evaluate(() => {
      const out = [];
      try {
        for (let i = 0; i < localStorage.length; i += 1) {
          const name = localStorage.key(i);
          if (!name) continue;
          out.push({ name, value: localStorage.getItem(name) ?? '' });
        }
      } catch {}
      return out;
    });
    if (Array.isArray(items) && items.length > 0) {
      xiaohongshuSavedLocalStorage = items;
    }
    return items || [];
  } catch {
    return [];
  }
}

async function applyXiaohongshuLocalStorage(page) {
  if (!page || page.isClosed() || !xiaohongshuSavedLocalStorage.length) return;
  try {
    await page.evaluate((items) => {
      try {
        for (const item of items) {
          if (!item?.name) continue;
          localStorage.setItem(item.name, item.value ?? '');
        }
      } catch {}
    }, xiaohongshuSavedLocalStorage);
    log(`applied xhs localStorage keys=${xiaohongshuSavedLocalStorage.length}`);
  } catch (error) {
    log(`apply xhs localStorage failed: ${error.message}`);
  }
}

async function readXiaohongshuCookieMap() {
  await startBrowser();
  const cookies = await context.cookies(['https://www.xiaohongshu.com', 'https://edith.xiaohongshu.com']);
  return new Map(cookies.map((c) => [c.name, c.value]));
}

/**
 * Persist browser state, but protect good XHS sessions from being wiped by
 * half-dead / guest / login-page scans.
 *
 * CRITICAL (2026-07-20): force=true still MUST NOT erase a known-good web_session
 * when the live context no longer has one. Only write a no-session snapshot when
 * there was never a good session (or reason is explicit wipe).
 * @param {{ force?: boolean, reason?: string }} opts
 */
async function persistStorageState(opts = {}) {
  if (!context || !statePath) return false;
  const force = Boolean(opts.force);
  const reason = String(opts.reason || 'unspecified');
  try {
    // Merge with on-disk snapshot so we can protect previous good XHS cookies.
    let diskState = null;
    try {
      diskState = JSON.parse(await fs.readFile(statePath, 'utf8'));
    } catch {}
    const diskCookies = Array.isArray(diskState?.cookies) ? diskState.cookies : [];
    const diskXhs = diskCookies.filter((c) => /xiaohongshu/i.test(String(c.domain || '')));
    const diskWs = diskXhs.find((c) => c.name === 'web_session')?.value || '';
    const diskA1 = diskXhs.find((c) => c.name === 'a1')?.value || '';

    const cookies = await context.cookies();
    const xhs = cookies.filter((c) => /xiaohongshu/i.test(String(c.domain || '')));
    let ws = xhs.find((c) => c.name === 'web_session')?.value || '';
    let a1 = xhs.find((c) => c.name === 'a1')?.value || '';
    let hasSession = Boolean(ws && a1);

    // If memory remembers a good session and live lost web_session, protect disk.
    const rememberedWs = xiaohongshuLastGoodSession?.webSession || diskWs || '';
    const rememberedA1 = xiaohongshuLastGoodSession?.a1 || diskA1 || '';
    if (!hasSession && rememberedWs) {
      // Never overwrite a good disk session with a no-web_session snapshot.
      log(`persistStorageState SKIP (refuse to wipe web_session) reason=${reason} force=${force} liveSession=false diskWs=${Boolean(diskWs)} remembered=true`);
      return false;
    }

    if (!force && xiaohongshuLoginDead && !hasSession) {
      log(`persistStorageState SKIP (login-dead, no session) reason=${reason}`);
      return false;
    }

    // Best-effort capture localStorage from live xhs page (if open on xhs host).
    try {
      const p = xiaohongshuPage && !xiaohongshuPage.isClosed() ? xiaohongshuPage : null;
      const url = p ? pageUrlSafe(p) : '';
      if (p && /xiaohongshu\.com/i.test(url) && !/website-login\/error|\/login/i.test(url)) {
        await captureXiaohongshuLocalStorage(p);
      }
    } catch {}

    // If live missing some xhs cookies but disk has them (and live still has session), merge disk→live first.
    if (hasSession && diskXhs.length > 0) {
      const liveNames = new Set(xhs.map((c) => `${c.domain}|${c.name}`));
      const toAdd = [];
      for (const c of diskXhs) {
        const key = `${c.domain}|${c.name}`;
        if (!liveNames.has(key) && c.name && c.value) {
          toAdd.push(c);
        }
      }
      if (toAdd.length > 0) {
        try {
          await context.addCookies(toAdd);
          log(`persistStorageState merged ${toAdd.length} missing xhs cookies from disk`);
        } catch {}
      }
    }

    const tempPath = `${statePath}.tmp`;
    await context.storageState({ path: tempPath });
    // Merge saved xhs localStorage into origins so restart restores more than cookies.
    try {
      const raw = await fs.readFile(tempPath, 'utf8');
      const state = JSON.parse(raw);
      if (!Array.isArray(state.origins)) state.origins = [];
      // Preserve disk xhs localStorage if live capture empty.
      if (xiaohongshuSavedLocalStorage.length === 0 && Array.isArray(diskState?.origins)) {
        for (const origin of diskState.origins) {
          if (/xiaohongshu\.com/i.test(String(origin?.origin || '')) && Array.isArray(origin.localStorage) && origin.localStorage.length) {
            xiaohongshuSavedLocalStorage = origin.localStorage
              .map((item) => ({ name: String(item.name || ''), value: String(item.value ?? '') }))
              .filter((item) => item.name);
          }
        }
      }
      if (xiaohongshuSavedLocalStorage.length > 0) {
        const originUrl = 'https://www.xiaohongshu.com';
        const others = state.origins.filter((o) => !/xiaohongshu\.com/i.test(String(o?.origin || '')));
        others.push({
          origin: originUrl,
          localStorage: xiaohongshuSavedLocalStorage.map((i) => ({ name: i.name, value: i.value })),
        });
        state.origins = others;
      }
      // Final guard: if serialized cookies lost web_session but we had one, abort write.
      const serXhs = (state.cookies || []).filter((c) => /xiaohongshu/i.test(String(c.domain || '')));
      const serWs = serXhs.find((c) => c.name === 'web_session')?.value || '';
      if (!serWs && rememberedWs) {
        log(`persistStorageState ABORT write (serialized snapshot missing web_session) reason=${reason}`);
        await fs.unlink(tempPath).catch(() => {});
        return false;
      }
      await fs.writeFile(tempPath, JSON.stringify(state), 'utf8');
    } catch (error) {
      log(`persistStorageState merge localStorage failed: ${error.message}`);
    }
    await fs.chmod(tempPath, 0o600).catch(() => {});
    await fs.rename(tempPath, statePath);

    // Re-read written session fingerprint.
    ws = (await context.cookies()).filter((c) => /xiaohongshu/i.test(String(c.domain || '')) && c.name === 'web_session')[0]?.value || ws;
    a1 = (await context.cookies()).filter((c) => /xiaohongshu/i.test(String(c.domain || '')) && c.name === 'a1')[0]?.value || a1;
    hasSession = Boolean(ws && a1);
    if (hasSession) {
      xiaohongshuLastGoodSession = {
        webSession: ws,
        a1,
        savedAt: Date.now(),
        source: reason,
      };
    }
    log(`persistStorageState ok reason=${reason} xhsCookies=${xhs.length} hasSession=${hasSession} lsKeys=${xiaohongshuSavedLocalStorage.length}`);
    return true;
  } catch (error) {
    log(`persistStorageState failed reason=${reason}: ${error.message}`);
    return false;
  }
}

/** Strong login check: cookies + user/me not guest (when page can call it). No forced navigation. */
async function xiaohongshuSessionHealthy() {
  const cookies = await readXiaohongshuCookieMap();
  if (!(cookies.get('a1') && cookies.get('web_session'))) return { ok: false, reason: 'missing a1/web_session' };
  try {
    const p = xiaohongshuPage && !xiaohongshuPage.isClosed() ? xiaohongshuPage : null;
    if (!p) return { ok: true, reason: 'cookies-only', soft: true };
    const url = pageUrlSafe(p);
    if (/website-login\/error|error_code=300012|\/login/i.test(url)) {
      return { ok: false, reason: `login/risk page: ${url.slice(0, 100)}` };
    }
    if (!/xiaohongshu\.com/i.test(url) || url === 'about:blank') {
      return { ok: true, reason: 'cookies-present-page-not-xhs', soft: true };
    }
    const me = await p.evaluate(async () => {
      try {
        const res = await fetch('https://edith.xiaohongshu.com/api/sns/web/v2/user/me', {
          credentials: 'include',
          headers: { accept: 'application/json, text/plain, */*', referer: location.href },
        });
        const body = await res.json().catch(() => ({}));
        return {
          status: res.status,
          code: body?.code,
          guest: body?.data?.guest,
          userId: body?.data?.user_id,
          msg: body?.msg,
        };
      } catch (e) {
        return { err: String(e?.message || e) };
      }
    });
    if (me?.err) return { ok: true, reason: `cookies-me-err:${me.err}`, soft: true, me };
    if (me?.guest === true) return { ok: false, reason: 'user/me guest=true', me };
    if (me?.code === -100 || me?.code === '-100' || /登录|login/i.test(String(me?.msg || ''))) {
      return { ok: false, reason: `user/me ${me.code} ${me.msg}`, me };
    }
    return { ok: true, reason: 'cookies+me', me };
  } catch (error) {
    return { ok: true, reason: `cookies-soft:${error.message}`, soft: true };
  }
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
      // Reduce external protocol / app-handler noise (Open xdg-open? dialogs).
      '--disable-features=DialMediaRouteProvider,MediaRouter,AudioServiceOutOfProcess,IsolateOrigins,site-per-process',
      '--disable-external-intent-requests',
      // Lean server profile: no software GPU / crashpad spam / mute audio.
      '--disable-gpu',
      '--disable-gpu-compositing',
      '--disable-software-rasterizer',
      '--mute-audio',
      '--disable-audio-output',
      '--disable-breakpad',
      '--disable-crash-reporter',
      // Keep in sync with intentional long-lived tabs (main/weibo + xhs + douyin IM).
      // Login/lookup pages are temporary and should not force a high permanent limit.
      '--renderer-process-limit=4',
      '--js-flags=--max-old-space-size=256',
      '--blink-settings=imagesEnabled=false',
      '--disk-cache-size=33554432',
    ];
    if (typeof process.getuid === 'function' && process.getuid() === 0) {
      chromiumArgs.push('--no-sandbox');
    }
    const launchOptions = {
      headless: settings.headless,
      viewport: { width: 1280, height: 720 },
      args: chromiumArgs,
    };
    // Optional proxy for XHS IP-risk bypass (BROWSER_PROXY_SERVER / proxyServer).
    const proxyServer = String(settings.proxyServer || '').trim();
    if (proxyServer) {
      launchOptions.proxy = { server: proxyServer };
      log(`browser proxy enabled: ${proxyServer}`);
    }
    context = await launchPersistentBrowser(profileDir, launchOptions);
    // Close accidental popups (deep-link fallout).
    context.on('page', (p) => {
      p.on('dialog', async (dialog) => {
        try { await dialog.dismiss(); } catch {}
      });
      p.on('popup', (popup) => {
        void popup.close().catch(() => {});
      });
    });
    await context.addInitScript(() => {
      // Block window.open to app schemes (snssdk/aweme/xhs…); these trigger xdg-open dialogs.
      const blockScheme = (url) => /^(snssdk|aweme|douyin|xhsdiscover|xhs|weixin|weibo|tbopen|market):/i.test(String(url || ''));
      const nativeOpen = window.open;
      window.open = function patchedOpen(url, ...rest) {
        if (blockScheme(url)) return null;
        return nativeOpen.call(this, url, ...rest);
      };
      // Intercept <a> clicks that try to leave to app protocols.
      document.addEventListener('click', (ev) => {
        const a = ev.target?.closest?.('a[href]');
        if (a && blockScheme(a.getAttribute('href') || a.href)) {
          ev.preventDefault();
          ev.stopPropagation();
        }
      }, true);
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
    // Abort app-scheme navigations (Playwright route only sees http(s) usually,
    // but keep as safety net when schemes surface as requests).
    await context.route('**/*', async (route) => {
      const url = route.request().url();
      if (/^(snssdk|aweme|douyin|xhsdiscover|xhs|weixin|weibo|tbopen|market):/i.test(url)) {
        await route.abort().catch(() => {});
        return;
      }
      await route.continue().catch(() => {});
    });
    await restoreStorageState();
    await seedConfiguredCookies();
    page = context.pages()[0] || await context.newPage();
    page.setDefaultTimeout(15_000);
    page.on('dialog', async (dialog) => {
      try { await dialog.dismiss(); } catch {}
    });
    // Drop leftover tabs from previous runs (profile can restore many pages).
    await pruneBrowserTabs({ reason: 'start' });
    // Disable automatic session-style multi-tab restore growth: blank main only.
    try {
      for (const p of context.pages()) {
        if (p !== page && !p.isClosed()) {
          const u = pageUrlSafe(p);
          // Keep nothing at cold start except the primary page; dedicated tabs open lazily.
          if (!/im\.douyin\.com|xiaohongshu\.com\/explore|\/login|captcha|verify|passport\./i.test(u)) {
            await p.close().catch(() => {});
          }
        }
      }
    } catch {}
    await logBrowserPagesDiag('start');
    scheduleBrowserHousekeeping();
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

async function getXiaohongshuPage() {
  await startBrowser();
  if (!xiaohongshuPage || xiaohongshuPage.isClosed()) {
    xiaohongshuPage = await context.newPage();
    xiaohongshuPage.setDefaultTimeout(15_000);
    attachXiaohongshuSignCapture(xiaohongshuPage);
    // Restore previously saved localStorage once the first xhs navigation happens.
    xiaohongshuPage.once('domcontentloaded', () => {
      void applyXiaohongshuLocalStorage(xiaohongshuPage).catch(() => {});
    });
  }
  return xiaohongshuPage;
}

// Long-lived tabs we intentionally keep. Everything else is a leak / leftover.
// douyinPage is only for QR login and is released after login finishes.
// douyinLookupPage is temporary and released after nickname lookup.
function browserKeepPages() {
  return [page, douyinPage, douyinIMPage, douyinLookupPage, xiaohongshuPage]
    .filter((p) => p && !p.isClosed());
}

function pageUrlSafe(p) {
  try { return p.url() || ''; } catch { return ''; }
}

function pageRole(p) {
  if (p === page) return 'main';
  if (p === douyinPage) return 'douyin-login';
  if (p === douyinIMPage) return 'douyin-im';
  if (p === douyinLookupPage) return 'douyin-lookup';
  if (p === xiaohongshuPage) return 'xiaohongshu';
  return 'orphan';
}

// A: diagnostic dump of open tabs + node heap (RSS still needs `ps` outside).
async function logBrowserPagesDiag(reason = 'manual') {
  if (!context) {
    log(`browserDiag(${reason}): no context`);
    return;
  }
  const keep = new Set(browserKeepPages());
  const pages = context.pages().filter((p) => p && !p.isClosed());
  const mem = process.memoryUsage();
  const rows = pages.map((p, i) => {
    const url = pageUrlSafe(p).slice(0, 120);
    return `#${i} role=${pageRole(p)} keep=${keep.has(p) ? 1 : 0} url=${url || '(empty)'}`;
  });
  log(
    `browserDiag(${reason}): pages=${pages.length} keep=${keep.size} `
    + `nodeHeapMB=${Math.round(mem.heapUsed / 1024 / 1024)} `
    + `nodeRssMB=${Math.round((mem.rss || 0) / 1024 / 1024)} `
    + `| ${rows.join(' || ') || '(none)'}`,
  );
}

function scheduleBrowserHousekeeping() {
  clearInterval(browserHousekeepTimer);
  // Periodic prune + diag to catch leaked tabs / growing node heap.
  browserHousekeepTimer = setInterval(() => {
    if (shuttingDown || !context) return;
    void (async () => {
      try {
        await pruneBrowserTabs({ reason: 'timer' });
        await logBrowserPagesDiag('timer');
      } catch (error) {
        log(`browser housekeeping failed: ${error.message}`);
      }
    })();
  }, 5 * 60_000);
}

// Aggressive prune: keep only known utility tabs + IM/login by URL if ref was lost.
// Close blank / old profile / orphan tabs to cut renderer RSS.
// NEVER close xiaohongshu.com tabs during scan — user may be mid login/captcha.
async function pruneBrowserTabs({ reason = 'manual' } = {}) {
  if (!context) return;
  const keep = new Set(browserKeepPages());
  let closed = 0;
  for (const p of context.pages()) {
    if (keep.has(p) || p.isClosed()) continue;
    const url = pageUrlSafe(p);
    // Protect IM / passport / QR even if our ref was lost after restart churn.
    if (/im\.douyin\.com|passport\.|\/login|qrcode|qr\.|scan|website-login|captcha|verify/i.test(url)) continue;
    // Protect ALL xhs pages when pruning from scan loop (login/captcha safety).
    if (reason === 'scan' && /xiaohongshu\.com/i.test(url)) {
      if (/xiaohongshu\.com\/explore/i.test(url) && !xiaohongshuPage) {
        xiaohongshuPage = p;
        attachXiaohongshuSignCapture(p);
      }
      keep.add(p);
      continue;
    }
    // Protect live xhs explore if our xiaohongshuPage ref was lost.
    if (/xiaohongshu\.com\/explore/i.test(url) && !xiaohongshuPage) {
      xiaohongshuPage = p;
      attachXiaohongshuSignCapture(p);
      keep.add(p);
      continue;
    }
    // Close: blank, chrome internals, old per-user profile pages, random leftovers.
    await p.close().catch(() => {});
    closed += 1;
  }
  if (closed > 0) log(`pruneBrowserTabs(${reason}): closed ${closed} orphan tab(s); keep=${keep.size}`);
  // If douyin lookup page was closed externally, drop ref.
  if (douyinLookupPage && douyinLookupPage.isClosed()) douyinLookupPage = undefined;
  if (douyinPage && douyinPage.isClosed()) douyinPage = undefined;
}

// Back-compat name used by scan loops.
async function pruneOrphanProfilePages() {
  await pruneBrowserTabs({ reason: 'scan' });
}

async function getDouyinIMPage() {
  await startBrowser();
  if (!douyinIMPage || douyinIMPage.isClosed()) {
    douyinIMPage = await context.newPage();
    douyinIMPage.setDefaultTimeout(15_000);
  }
  return douyinIMPage;
}

async function getDouyinLookupPage() {
  await startBrowser();
  if (!douyinLookupPage || douyinLookupPage.isClosed()) {
    douyinLookupPage = await context.newPage();
    douyinLookupPage.setDefaultTimeout(15_000);
  }
  return douyinLookupPage;
}

// After nickname lookup finishes, prefer closing the temp page so it does not
// sit as an idle multi-hundred-MB renderer.
async function releaseDouyinLookupPage() {
  if (!douyinLookupPage || douyinLookupPage.isClosed()) {
    douyinLookupPage = undefined;
    return;
  }
  try { await douyinLookupPage.close(); } catch {}
  douyinLookupPage = undefined;
}

// Douyin works path is Cookie+HTTP only. Login page is temporary — close after QR flow.
async function releaseDouyinPage(reason = 'manual') {
  if (!douyinPage || douyinPage.isClosed()) {
    douyinPage = undefined;
    return;
  }
  try { await douyinPage.close(); } catch {}
  douyinPage = undefined;
  log(`releaseDouyinPage(${reason}): closed temporary douyin login tab`);
}

async function syncDouyinContacts({ force = false } = {}) {
  if (douyinContactSyncRunning || shuttingDown || !settings.douyinIMEnabled) return;
  // Prefer on-disk cache unless force / stale / empty.
  if (!force) {
    try {
      const cachePath = douyinContactCachePath();
      const raw = await fs.readFile(cachePath, 'utf8').catch(() => '');
      if (raw) {
        const saved = JSON.parse(raw);
        const contacts = Array.isArray(saved?.contacts) ? saved.contacts : [];
        const updatedAt = Number(saved?.updatedAt || 0);
        const ageMs = Date.now() - updatedAt;
        if (contacts.length > 0 && updatedAt > 0 && ageMs >= 0 && ageMs < douyinContactSyncMs) {
          log(`skip Douyin contact sync: cache fresh (${contacts.length} contacts, age=${Math.round(ageMs / 60_000)}m)`);
          return;
        }
      }
    } catch {}
  } else {
    log('Douyin contact sync forced');
  }

  douyinContactSyncRunning = true;
  const seen = new Set();
  let mutualCount = 0;
  let pageCount = 0;
  let targetPage;
  try {
    await startBrowser();
    targetPage = await getDouyinIMPage();
    // Ensure we are on douyin so same-origin fetch carries cookies + a_bogus helpers.
    if (!/douyin\.com/i.test(targetPage.url() || '')) {
      await targetPage.goto('https://www.douyin.com/', { waitUntil: 'domcontentloaded', timeout: 45_000 }).catch(() => {});
      await targetPage.waitForTimeout(800);
    }

    const ingest = (users = []) => {
      for (const user of users) {
        const uid = String(user?.uid || user?.user_id || '').trim();
        const secUid = String(user?.sec_uid || user?.sec_user_id || '').trim();
        const nickname = String(user?.nickname || '').trim();
        if ((!uid && !secUid) || !nickname) continue;
        const mutual = Number(user?.follow_status) === 2;
        const key = uid ? `uid:${uid}` : `sec:${secUid}`;
        seen.add(key);
        if (mutual) mutualCount += 1;
        // IMPORTANT: Web following/list often returns remark_name=null.
        // Only pass remarkName when non-empty so rememberDouyinContact keeps
        // previously persisted remarks (e.g. 葡萄吞十七 → 唐欣怡).
        const rem = String(user?.remark_name ?? user?.remarkName ?? user?.remark ?? '').trim();
        const payload = {
          uid,
          secUid,
          nickname,
          mutual,
          updatedAt: Date.now(),
        };
        if (rem) payload.remarkName = rem;
        rememberDouyinContact(payload);
      }
    };

    // Primary: in-page fetch against following list API (more reliable than UI click).
    // UI click path is kept as fallback when API path returns nothing.
    let offset = 0;
    let hasMore = true;
    let apiPages = 0;
    let lastStatus = null;
    while (hasMore && apiPages < 40) {
      const pageResult = await targetPage.evaluate(async ({ offset: off, count, userId, secUserId }) => {
        try {
          const params = new URLSearchParams({
            device_platform: 'webapp',
            aid: '6383',
            channel: 'channel_pc_web',
            count: String(count),
            offset: String(off),
            source_type: '4',
          });
          if (userId) params.set('user_id', String(userId));
          if (secUserId) params.set('sec_user_id', String(secUserId));
          const response = await fetch(`/aweme/v1/web/user/following/list/?${params.toString()}`, {
            credentials: 'include',
            headers: { accept: 'application/json, text/plain, */*' },
          });
          const body = await response.json().catch(() => null);
          return {
            ok: true,
            http: response.status,
            status_code: body?.status_code,
            has_more: body?.has_more === true || body?.has_more === 1,
            followings: Array.isArray(body?.followings) ? body.followings : [],
            total: body?.total,
            message: body?.status_msg || body?.message || '',
          };
        } catch (error) {
          return { ok: false, error: String(error?.message || error) };
        }
      }, {
        offset,
        count: 20,
        // Self uid from identity cookie/render; empty is ok — caller may still fall back to UI.
        userId: douyinIMIdentity?.selfUid || '',
        secUserId: '',
      });

      lastStatus = pageResult;
      if (!pageResult?.ok) throw new Error(`following list fetch failed: ${pageResult?.error || 'unknown'}`);
      if (Number(pageResult.status_code) !== 0 && pageResult.followings.length === 0) {
        throw new Error(`following list status_code=${pageResult.status_code} http=${pageResult.http} msg=${pageResult.message || '-'}`);
      }
      if (pageResult.followings.length === 0) break;
      ingest(pageResult.followings);
      apiPages += 1;
      pageCount = apiPages;
      hasMore = pageResult.has_more === true;
      offset += pageResult.followings.length;
      if (!hasMore) break;
      await targetPage.waitForTimeout(250);
    }

    // Fallback: intercept UI-driven responses if API path produced nothing.
    if (seen.size === 0) {
      log('Douyin contact API returned empty; falling back to UI intercept');
      const responseTasks = new Map();
      const onResponse = (response) => {
        if (!response.url().includes('/aweme/v1/web/user/following/list/')) return Promise.resolve();
        if (responseTasks.has(response.url())) return responseTasks.get(response.url());
        const task = (async () => {
          try {
            const body = await response.json();
            if (Number(body?.status_code) !== 0 || !Array.isArray(body?.followings)) return;
            pageCount += 1;
            ingest(body.followings);
          } catch (error) {
            log(`Douyin following page decode failed: ${error.message}`);
          }
        })();
        responseTasks.set(response.url(), task);
        return task;
      };
      targetPage.on('response', onResponse);
      try {
        await targetPage.goto('https://www.douyin.com/user/self', {
          waitUntil: 'domcontentloaded',
          timeout: 30_000,
        });
        const firstResponseResult = targetPage.waitForResponse(
          (response) => response.url().includes('/aweme/v1/web/user/following/list/'),
          { timeout: 20_000 },
        ).then((response) => ({ response }), (error) => ({ error }));
        const followSelectors = [
          '[data-e2e="user-info-follow"] > div',
          '[data-e2e="user-info-follow"]',
          'text=关注',
          'div:has-text("关注")',
        ];
        for (const sel of followSelectors) {
          try {
            const loc = targetPage.locator(sel).last();
            if (await loc.count() === 0) continue;
            await loc.click({ timeout: 8_000 });
            break;
          } catch {}
        }
        const firstResult = await firstResponseResult;
        if (!firstResult.error) await onResponse(firstResult.response);
        await targetPage.waitForTimeout(1_500);
        for (let i = 0; i < 8; i++) {
          await targetPage.locator('div').evaluateAll((elements) => {
            const scrollable = elements
              .filter((element) => element.clientHeight > 200 && element.scrollHeight > element.clientHeight + 500)
              .sort((left, right) => right.scrollHeight - left.scrollHeight)[0];
            if (scrollable) scrollable.scrollTop = scrollable.scrollHeight;
          });
          await targetPage.waitForTimeout(700);
        }
      } finally {
        targetPage.off('response', onResponse);
      }
    }

    if (pageCount === 0 || seen.size === 0) {
      throw new Error(`关注列表未返回有效分页 last=${JSON.stringify(lastStatus || {}).slice(0, 240)}`);
    }
    await persistDouyinContacts();
    log(`synced ${seen.size} Douyin following contacts (${mutualCount} mutual) across ${pageCount} pages`);
  } catch (error) {
    const pageInfo = targetPage ? ` page=${targetPage.url()} title=${await targetPage.title().catch(() => '-')}` : '';
    log(`Douyin contact sync failed: ${error.message}${pageInfo}`);
    const cacheCount = [...douyinContacts.values()].length;
    if (!shuttingDown && settings.douyinIMEnabled && cacheCount === 0) {
      setTimeout(() => void syncDouyinContacts({ force: true }), 10 * 60_000);
    }
  } finally {
    douyinContactSyncRunning = false;
  }
}

function scheduleDouyinContactSync() {
  clearInterval(douyinContactSyncTimer);
  douyinContactSyncTimer = undefined;
  if (!settings.douyinIMEnabled) return;
  douyinContactSyncTimer = setInterval(() => void syncDouyinContacts(), douyinContactSyncMs);
  setTimeout(() => void syncDouyinContacts(), 5 * 60_000);
}

function stopDouyinIM() {
  clearTimeout(douyinIMReconnectTimer);
  douyinIMReconnectTimer = undefined;
  clearTimeout(douyinIMInitRetryTimer);
  douyinIMInitRetryTimer = undefined;
  clearInterval(douyinIMHealthTimer);
  douyinIMHealthTimer = undefined;
  clearInterval(douyinContactSyncTimer);
  douyinContactSyncTimer = undefined;
  if (douyinIMSocket) {
    const socket = douyinIMSocket;
    douyinIMSocket = undefined;
    socket.removeAllListeners();
    socket.close();
  }
  douyinIMIdentity = { selfUid: '', conversationId: '', ownerUid: '', groupName: '', groupNumber: '' };
}

function douyinIMIdentityPath() {
  return path.resolve(settings.profileDir, 'douyin-im-identity.json');
}

async function persistDouyinIMIdentity() {
  if (!douyinIMIdentity.selfUid || !douyinIMIdentity.conversationId) return;
  const target = douyinIMIdentityPath();
  await fs.mkdir(path.dirname(target), { recursive: true });
  const temp = `${target}.tmp`;
  await fs.writeFile(temp, JSON.stringify(douyinIMIdentity), { mode: 0o600 });
  await fs.rename(temp, target);
}

function emitDouyinIMGroupIdentity() {
  if (!douyinIMIdentity?.selfUid || !douyinIMIdentity?.conversationId || !douyinIMIdentity?.ownerUid) return;
  emit('douyin_im_group', {
    groupName: douyinIMIdentity.groupName,
    groupNumber: douyinIMIdentity.groupNumber,
    conversationId: douyinIMIdentity.conversationId,
    ownerUid: douyinIMIdentity.ownerUid,
    selfUid: douyinIMIdentity.selfUid,
  });
}

async function restoreDouyinIMIdentity() {
  try {
    const restored = JSON.parse(await fs.readFile(douyinIMIdentityPath(), 'utf8'));
    const configuredNumber = String(settings.douyinIMGroupNumber || '').trim();
    const configuredName = String(settings.douyinIMGroupName || '').trim();
    const numberMatches = !configuredNumber || String(restored.groupNumber || '') === configuredNumber;
    const nameMatches = !configuredName || String(restored.groupName || '').includes(configuredName);
    if (!/^\d+$/.test(String(restored.selfUid || '')) || !restored.conversationId || !numberMatches || !nameMatches) return false;
    douyinIMIdentity = {
      selfUid: String(restored.selfUid),
      conversationId: String(restored.conversationId),
      ownerUid: String(restored.ownerUid || ''),
      groupName: String(restored.groupName || configuredName),
      groupNumber: String(restored.groupNumber || configuredNumber),
    };
    log(`restored Douyin IM identity for group ${douyinIMIdentity.groupName || douyinIMIdentity.groupNumber}`);
    // Critical: Go keeps owner/conversation only in memory. After bot restart the
    // sidecar often restores from disk and skips full IM init, so we must re-emit
    // group metadata or every group_owner message is silently dropped.
    emitDouyinIMGroupIdentity();
    return true;
  } catch {
    return false;
  }
}

function scheduleDouyinIMInitRetry() {
  clearTimeout(douyinIMInitRetryTimer);
  if (shuttingDown || !settings.douyinIMEnabled || douyinIMIdentity.selfUid) return;
  douyinIMInitRetryTimer = setTimeout(() => {
    douyinIMInitRetryTimer = undefined;
    void startDouyinIM();
  }, 60_000);
}

// Prevent QR spam when reconnect storms happen (e.g. flaky WS).
let douyinIMLoginRequestAt = 0;

async function ensureDouyinLoginForIM(reason = 'im') {
  if (await douyinBrowserLoggedIn()) return true;
  const now = Date.now();
  if (now - douyinIMLoginRequestAt < 90_000) {
    log(`Douyin IM needs login (${reason}) but QR request cooled down`);
    emit('douyin_status', { status: 'login_required', message: '抖音浏览器需要登录（IM 重连）' });
    emit('douyin_im_status', {
      status: 'disconnected',
      message: '抖音登录失效，已请求扫码；请打开面板浏览器扫码登录',
    });
    return false;
  }
  douyinIMLoginRequestAt = now;
  log(`Douyin IM needs login (${reason}) → request QR`);
  emit('douyin_status', { status: 'login_required', message: '抖音浏览器需要登录（IM 重连）' });
  emit('douyin_im_status', {
    status: 'disconnected',
    message: '抖音登录失效，正在打开扫码页，请打开面板浏览器扫码',
  });
  try {
    await requestDouyinLoginQRCode();
  } catch (error) {
    log(`Douyin IM login QR failed: ${error.message}`);
  }
  return douyinBrowserLoggedIn();
}

function scheduleDouyinIMReconnect() {
  clearTimeout(douyinIMReconnectTimer);
  if (shuttingDown || !settings.douyinIMEnabled) return;
  douyinIMReconnectTimer = setTimeout(() => {
    douyinIMReconnectTimer = undefined;
    void (async () => {
      const ok = await ensureDouyinLoginForIM('reconnect');
      if (!ok) {
        // Keep retrying so after user scans we reconnect without manual restart.
        scheduleDouyinIMReconnect();
        return;
      }
      // Prefer full start if identity was lost; otherwise pure socket reconnect.
      if (!douyinIMIdentity.selfUid) {
        await startDouyinIM();
      } else {
        await connectDouyinIM();
      }
    })();
  }, 5_000);
}

function rememberDouyinIMMessage(key, softKey = '') {
  if (key && douyinIMSeen.has(key)) return false;
  if (softKey && douyinIMSeen.has(softKey)) return false;
  if (key) {
    douyinIMSeen.add(key);
    if (douyinIMSeen.size > 5_000) douyinIMSeen.delete(douyinIMSeen.values().next().value);
  }
  if (softKey) {
    douyinIMSeen.add(softKey);
    // Soft keys expire: drop after a while by also storing timestamped cleanup
    if (douyinIMSeen.size > 5_000) douyinIMSeen.delete(douyinIMSeen.values().next().value);
  }
  return true;
}

// Cache recent private messages so reply frames that only carry a ref id can be resolved
// to "我：原文" / peer text. Own outbound messages were previously dropped entirely.
const douyinIMTextByServerId = new Map();
const douyinIMTextByClientId = new Map();
const DOUYIN_IM_TEXT_CACHE_MAX = 800;

function rememberDouyinIMText(message, { isSelf = false } = {}) {
  if (!message) return;
  let text = String(message.text || '').trim();
  // Remember image / sticker placeholders so later reply frames can quote 「我：[图片]」.
  if (!text && Array.isArray(message.images) && message.images.length) {
    text = '[图片]';
  }
  if (!text || text === '[暂不支持的消息]' || text === '[系统提示]' || message.internalMetadata) return;
  // Do not cache garbage sec_uid tokens as quote text.
  if (/^MS4wLjABAAAA/i.test(text) || (/^[A-Za-z0-9_-]{40,}$/.test(text) && !/[\u4e00-\u9fff]/.test(text))) return;
  const entry = {
    text,
    senderUid: String(message.senderUid || ''),
    conversationId: String(message.conversationId || ''),
    isSelf: Boolean(isSelf),
    at: Date.now(),
  };
  const serverId = String(message.serverMessageId || '').trim();
  const clientId = String(
    message.clientMessageId
    || message.extSummary?.['s:client_message_id']
    || '',
  ).trim();
  if (serverId) {
    douyinIMTextByServerId.set(serverId, entry);
    if (douyinIMTextByServerId.size > DOUYIN_IM_TEXT_CACHE_MAX) {
      douyinIMTextByServerId.delete(douyinIMTextByServerId.keys().next().value);
    }
  }
  if (clientId) {
    douyinIMTextByClientId.set(clientId, entry);
    if (douyinIMTextByClientId.size > DOUYIN_IM_TEXT_CACHE_MAX) {
      douyinIMTextByClientId.delete(douyinIMTextByClientId.keys().next().value);
    }
  }
}

function lookupDouyinIMQuotedText(message) {
  if (!message) return null;
  const serverId = String(message.quotedServerMessageId || '').trim();
  const clientId = String(message.quotedClientMessageId || '').trim();
  if (serverId && douyinIMTextByServerId.has(serverId)) return douyinIMTextByServerId.get(serverId);
  if (clientId && douyinIMTextByClientId.has(clientId)) return douyinIMTextByClientId.get(clientId);
  return null;
}

function enrichDouyinIMQuote(message) {
  if (!message) return message;
  let quotedText = String(message.quotedText || '').trim();
  let quotedName = String(message.quotedName || '').trim();
  let quotedSenderUid = String(message.quotedSenderUid || '').trim();
  // Drop sec_uid / opaque / bare-integer garbage quotes before enrichment.
  if (
    /^MS4wLjABAAAA/i.test(quotedText)
    || (/^[A-Za-z0-9_-]{40,}$/.test(quotedText) && !/[\u4e00-\u9fff]/.test(quotedText))
    || /^\d{1,6}$/.test(quotedText)
    || /^\d{10,}$/.test(quotedText)
  ) {
    quotedText = '';
  }
  const cached = lookupDouyinIMQuotedText(message);
  if (cached) {
    if (!quotedText) quotedText = compactDouyinShareQuote(cached.text) || cached.text;
    if (!quotedSenderUid && cached.senderUid) quotedSenderUid = cached.senderUid;
    if (!quotedName) {
      if (cached.isSelf || (douyinIMIdentity.selfUid && cached.senderUid === douyinIMIdentity.selfUid)) {
        quotedName = '我';
      }
    }
  }
  // Resolve quote speaker from recent text cache when protocol only gives quote body.
  // Match within the SAME conversation first: the same short text (e.g. 笑死我了) can
  // exist in multiple DMs from different senders, and cross-conv matching mislabels
  // the quote speaker (real sample 2026-08-04: own message quoted as 张若昀).
  if (quotedText && !quotedName) {
    const conv = String(message.conversationId || '').trim();
    const candidates = [];
    for (const entry of douyinIMTextByServerId.values()) {
      if (!entry?.text) continue;
      const et = String(entry.text).trim();
      if (et === quotedText || et.startsWith(quotedText) || quotedText.startsWith(et.slice(0, 20))) {
        const score = conv && entry.conversationId === conv ? 0 : 1;
        candidates.push({ score, entry });
      }
    }
    candidates.sort((a, b) => a.score - b.score || b.entry.at - a.entry.at);
    for (const { entry } of candidates) {
      if (entry.isSelf || (douyinIMIdentity.selfUid && entry.senderUid === douyinIMIdentity.selfUid)) {
        quotedName = '我';
      } else if (entry.senderUid) {
        const id = resolveDouyinSenderIdentity('', entry.senderUid, '');
        quotedName = id.remarkName || id.nickname || '';
        if (!quotedSenderUid) quotedSenderUid = entry.senderUid;
      }
      if (quotedName) break;
    }
  }
  // Contact book: if we have quote uid, prefer remark/nick.
  if (quotedSenderUid && !quotedName) {
    const id = resolveDouyinSenderIdentity('', quotedSenderUid, '');
    quotedName = id.remarkName || id.nickname || '';
  }
  // If still no quote text but we have a ref id that wasn't cached: leave empty.
  if (!quotedText && (message.quotedServerMessageId || message.quotedClientMessageId)) {
    // leave empty — better no quote than chip id
  }
  // Heuristic for type=7 frames that only carry nested share-card text as quote:
  // if quotedText is a share card and quote uid still unknown, prefer marking as self
  // when we ourselves recently sent the same share (text cache by content match).
  // Also: quotedSenderUid is often the *content author* of the shared post, not the
  // IM sender — if cache says we sent this card, force 「我」even when uid differs.
  if (quotedText && looksLikeDouyinShareCardText(quotedText)) {
    const compact = compactDouyinShareQuote(quotedText) || quotedText;
    for (const entry of douyinIMTextByServerId.values()) {
      if (!entry?.isSelf) continue;
      const et = compactDouyinShareQuote(entry.text) || entry.text;
      if (
        et === compact
        || entry.text === quotedText
        || (compact && String(entry.text || '').includes(compact.replace(/^\[分享图文\]\s*/, '').slice(0, 20)))
        || (compact && String(et || '').includes(String(quotedText).replace(/^\[分享图文\]\s*/, '').slice(0, 20)))
      ) {
        quotedName = '我';
        quotedSenderUid = String(entry.senderUid || douyinIMIdentity.selfUid || '');
        quotedText = compact || quotedText;
        break;
      }
    }
  }
  if (quotedText && !quotedSenderUid && !quotedName) {
    const compact = compactDouyinShareQuote(quotedText);
    for (const entry of douyinIMTextByServerId.values()) {
      if (!entry?.isSelf) continue;
      const et = compactDouyinShareQuote(entry.text) || entry.text;
      if (et === compact || entry.text === quotedText || (compact && String(entry.text || '').startsWith(compact))) {
        quotedName = '我';
        quotedSenderUid = String(entry.senderUid || douyinIMIdentity.selfUid || '');
        quotedText = compact || quotedText;
        break;
      }
    }
  }
  if (!quotedName && quotedSenderUid && douyinIMIdentity.selfUid && quotedSenderUid === douyinIMIdentity.selfUid) {
    quotedName = '我';
  }
  // Normalize video/share quote labels (keep title when present).
  if (quotedText) quotedText = compactDouyinShareQuote(quotedText) || quotedText;
  // If quote is bare [视频] and cache has a richer video line, upgrade.
  if (quotedText === '[视频]' || quotedText === '[分享图文]') {
    const cached2 = lookupDouyinIMQuotedText(message);
    if (cached2?.text) {
      const richer = compactDouyinShareQuote(cached2.text) || cached2.text;
      if (richer && richer !== quotedText && richer.length > quotedText.length) {
        quotedText = richer;
      }
    }
  }
  // Final sanitize (chip ids may reappear from cache).
  if (/^\d{1,6}$/.test(quotedText) || /^\d{10,}$/.test(quotedText)) quotedText = '';
  return {
    ...message,
    quotedText,
    quotedName,
    quotedSenderUid,
  };
}

async function resolveDouyinNickname(secUserId, userId) {
  const sec = String(secUserId || '').trim();
  const uid = String(userId || '').trim();
  if (!sec && !uid) return '';
  const cacheKey = sec ? `sec:${sec}` : `uid:${uid}`;
  if (douyinNicknameCache.has(cacheKey)) return douyinNicknameCache.get(cacheKey);
  try {
    const targetPage = await getDouyinIMPage();
    const cachedIdentity = await targetPage.evaluate(async ({ secUserId: secValue, userId: uidValue }) => {
      const nicknameKeys = ['remark_name', 'remarkName', 'nickname', 'nick_name', 'nickName', 'user_nickname', 'userNickname'];
      const findIdentity = (root, depth = 0) => {
        if (!root || typeof root !== 'object' || depth > 7) return { nickname: '', accountID: '' };
        let accountID = String(root.unique_id || root.uniqueId || root.short_id || root.shortId || '').trim();
        for (const key of nicknameKeys) {
          if (typeof root[key] === 'string' && root[key].trim()) {
            return { nickname: root[key].trim(), accountID };
          }
        }
        for (const child of Object.values(root)) {
          const found = findIdentity(child, depth + 1);
          if (found.nickname) return found;
          if (!accountID && found.accountID) accountID = found.accountID;
        }
        return { nickname: '', accountID };
      };
      const findTargetIdentity = (root, depth = 0) => {
        if (!root || typeof root !== 'object' || depth > 10) return { nickname: '', accountID: '' };
        const objectUID = String(root.uid || root.user_id || root.userId || '').trim();
        const objectSecUID = String(root.sec_uid || root.sec_user_id || root.secUserId || '').trim();
        if ((uidValue && objectUID === uidValue) || (secValue && objectSecUID === secValue)) {
          return findIdentity(root);
        }
        let accountID = '';
        for (const child of Object.values(root)) {
          const found = findTargetIdentity(child, depth + 1);
          if (found.nickname) return found;
          if (!accountID && found.accountID) accountID = found.accountID;
        }
        return { nickname: '', accountID };
      };
      const matchesTarget = (value) => {
        try {
          const serialized = JSON.stringify(value);
          return (uidValue && serialized.includes(uidValue)) || (secValue && serialized.includes(secValue));
        } catch { return false; }
      };
      const readRequest = (request) => new Promise((resolve) => {
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => resolve(undefined);
      });
      let accountID = '';
      for (const value of Object.values(localStorage)) {
        if (!String(value).includes(uidValue) && (!secValue || !String(value).includes(secValue))) continue;
        try {
          const found = findTargetIdentity(JSON.parse(value));
          if (found.nickname) return found;
          if (!accountID) accountID = found.accountID;
        } catch {}
      }
      for (const databaseInfo of await indexedDB.databases()) {
        if (!databaseInfo.name) continue;
        const database = await new Promise((resolve) => {
          const request = indexedDB.open(databaseInfo.name);
          request.onsuccess = () => resolve(request.result);
          request.onerror = () => resolve(undefined);
        });
        if (!database) continue;
        for (const storeName of Array.from(database.objectStoreNames)) {
          for (const key of [`${uidValue}_user`, uidValue, secValue].filter(Boolean)) {
            try {
              const transaction = database.transaction(storeName, 'readonly');
              const value = await readRequest(transaction.objectStore(storeName).get(key));
              const found = findTargetIdentity(value);
              if (found.nickname) { database.close(); return found; }
              if (!accountID) accountID = found.accountID;
            } catch {}
          }
          try {
            const transaction = database.transaction(storeName, 'readonly');
            const store = transaction.objectStore(storeName);
            const found = await new Promise((resolve) => {
              let visited = 0;
              const request = store.openCursor();
              request.onerror = () => resolve(undefined);
              request.onsuccess = () => {
                const cursor = request.result;
                if (!cursor || visited >= 20_000) return resolve(undefined);
                visited += 1;
                if (matchesTarget(cursor.value)) return resolve(findTargetIdentity(cursor.value));
                cursor.continue();
              };
            });
            if (found?.nickname) { database.close(); return found; }
            if (!accountID && found?.accountID) accountID = found.accountID;
          } catch {}
        }
        database.close();
      }
      return { nickname: '', accountID };
    }, { secUserId: sec, userId: uid });
    if (cachedIdentity.nickname) {
      rememberDouyinContact({ uid, secUid: sec, nickname: cachedIdentity.nickname }, { persist: true });
      return cachedIdentity.nickname;
    }
    const nickname = await targetPage.evaluate(async ({ secUserId: secValue, userId: uidValue }) => {
      const params = new URLSearchParams();
      if (secValue) params.set('sec_user_id', secValue);
      else params.set('user_id', uidValue);
      const response = await fetch(`/aweme/v1/web/user/profile/other/?${params}`, { credentials: 'include' });
      if (!response.ok) return '';
      const body = await response.json();
      return String(body?.user?.nickname || body?.user_info?.nickname || body?.data?.user?.nickname || '');
    }, { secUserId: sec, userId: uid });
    if (nickname) {
      rememberDouyinContact({ uid, secUid: sec, nickname }, { persist: true });
      return nickname;
    }
    if (sec) {
      const lookupPage = await getDouyinLookupPage();
      try {
        const profileResponse = lookupPage.waitForResponse(
          (response) => response.url().includes('/aweme/v1/web/user/profile/other/'),
          { timeout: 15_000 },
        ).catch(() => undefined);
        await lookupPage.goto(`https://www.douyin.com/user/${encodeURIComponent(sec)}`, {
          waitUntil: 'domcontentloaded',
          timeout: 20_000,
        }).catch(() => {});
        const response = await profileResponse;
        const profileNickname = await response?.json().then((body) => String(
          body?.user?.nickname || body?.user_info?.nickname || body?.data?.user?.nickname || '',
        )).catch(() => '');
        const renderedNickname = profileNickname || await lookupPage.locator('[data-e2e="user-title"], h1')
          .first().textContent({ timeout: 5_000 }).then((value) => String(value || '').trim()).catch(() => '');
        const pageTitleNickname = await lookupPage.title().then((value) => {
          const match = String(value || '').trim().match(/^(.+?)的抖音(?:主页)?/);
          return match?.[1]?.trim() || '';
        }).catch(() => '');
        const navigatedNickname = renderedNickname || pageTitleNickname;
        if (navigatedNickname && !['抖音', '抖音精选'].includes(navigatedNickname)) {
          rememberDouyinContact({ uid, secUid: sec, nickname: navigatedNickname }, { persist: true });
          return navigatedNickname;
        }
      } finally {
        // Do not keep a heavy profile renderer idle between lookups.
        await releaseDouyinLookupPage();
      }
    }
    const fallback = cachedIdentity.accountID ? `抖音号 ${cachedIdentity.accountID}` : '';
    if (fallback) douyinNicknameCache.set(cacheKey, fallback);
    return fallback;
  } catch {
    return '';
  }
}

async function publishDouyinIMMessage(message) {
  if (!message?.conversationId || !message.senderUid) return;
  // Always cache chat body for later reply resolution (including own messages).
  const isOwn = isOwnDouyinIMMessage(message.senderUid, douyinIMIdentity.selfUid);
  if (!message.internalMetadata) {
    rememberDouyinIMText(message, { isSelf: isOwn });
  }
  if (message.internalMetadata) return;

  const isPrivate = message.conversationType === 1 && settings.douyinIMPrivateEnabled;
  const isTargetGroup = message.conversationType === 2
    && message.conversationId === douyinIMIdentity.conversationId;

  // Learn private peers from *incoming* messages so we can refuse forwarding own
  // messages into other people's DMs (only allow notes-to-self).
  if (isPrivate && !isOwn) {
    rememberDouyinPrivatePeer(message.conversationId, message.senderUid, { isSelf: false });
  }

  // Own messages: only forward private notes-to-self, never own messages to peers/groups.
  if (isOwn) {
    const allowSelf = isPrivate && shouldForwardOwnPrivate(message, douyinIMIdentity.selfUid);
    if (!allowSelf) {
      if (isPrivate) {
        log(
          `Douyin IM skip own-private (not self-chat) conv=${message.conversationId}`
          + ` short=${message.conversationShortId || '-'}`
          + ` type=${message.messageType}`,
        );
      }
      return;
    }
    message = { ...message, isSelfChat: true };
    log(`Douyin IM allow own self-chat conv=${message.conversationId} type=${message.messageType}`);
  }
  if (!isPrivate && !isTargetGroup) return;

  // Sparse video share (type 8/77/105): field 8 JSON often empty on private shares
  // (contentLen=0). Douyin then sends the real caption as a separate type=7 with
  // quotedText="[视频]". Forwarding bare "[视频]" produces a useless second QQ bubble.
  // Exception: if we can recover a link/itemId (e.g. ext a:share_item_id), keep it.
  const bareVideo = String(message.text || '').trim() === '[视频]';
  const sparseVideoShare = [8, 77, 105].includes(Number(message.messageType))
    && bareVideo
    && !(Array.isArray(message.images) && message.images.length)
    && !String(message.link || '').trim();
  if (sparseVideoShare) {
    log(
      `Douyin IM suppress sparse video type=${message.messageType}`
      + ` convType=${message.conversationType}`
      + ` sender=${message.senderUid}`
      + ` contentLen=${message.contentLen ?? 0}`
      + ` keys=${message.contentKeys?.join(',') || '-'}`,
    );
    return;
  }

  // Strip empty media quotes so caption-only replies don't show 「[视频]」stack.
  // Also strip garbage sec_uid quotes.
  {
    const qt = String(message.quotedText || '').trim();
    const badQuote = qt === '[视频]'
      || /^MS4wLjABAAAA/i.test(qt)
      || (/^[A-Za-z0-9_-]{40,}$/.test(qt) && !/[\u4e00-\u9fff]/.test(qt));
    if (
      badQuote
      && !String(message.quotedName || '').trim()
    ) {
      message = { ...message, quotedText: '', quotedName: message.quotedName || '', quotedSenderUid: message.quotedSenderUid || '' };
      if (/^MS4wLjABAAAA/i.test(qt)) {
        // keep ids for cache lookup; only clear display text
        message = { ...message, quotedText: '' };
      }
    }
  }

  const key = message.serverMessageId || `${message.conversationId}:${message.index}`;
  // Douyin often double-pushes the same private text (sync + realtime) with different
  // server/client ids within ~3s. Soft-dedupe by conversation+sender+text(+quote) bucket.
  const textKey = String(message.text || '').trim().slice(0, 80);
  const quoteKey = String(message.quotedText || '').trim().slice(0, 40);
  const softBucket = Math.floor(Date.now() / 3000);
  const softPayload = `${textKey}|${quoteKey}`;
  const softKey = (isPrivate && softPayload && softPayload !== '|' && softPayload !== '[暂不支持的消息]|')
    ? `soft:${message.conversationId}:${message.senderUid}:${softPayload}:${softBucket}`
    : '';
  if (!rememberDouyinIMMessage(key, softKey)) return;
  message = enrichDouyinIMQuote(message);
  const displayFallback = cachedDouyinDisplayName(message.senderSecUid, message.senderUid)
    || message.senderNameHint
    || await resolveDouyinNickname(message.senderSecUid, message.senderUid);
  const identity = resolveDouyinSenderIdentity(message.senderSecUid, message.senderUid, displayFallback);
  const senderName = identity.remarkName || identity.nickname || displayFallback || '';
  const senderNickname = identity.nickname || displayFallback || '';
  const senderRemark = identity.remarkName || '';
  if (!senderName) log(`Douyin IM nickname unresolved sender_uid=${message.senderUid} sender_sec_uid=${message.senderSecUid || '-'}`);
  // Diagnostics: always log quote extraction for private type-7 replies; full dump when body unsupported or quote empty but refs present.
  const hasQuoteRef = Boolean(message.quotedText || message.quotedName || message.quotedServerMessageId || message.quotedClientMessageId);
  const isPlaceholderMedia = (
    (message.messageType === 27 && message.text === '[图片]' && !(message.images && message.images.length))
    || ([5, 50002, 70002].includes(message.messageType)
      && (message.text === '[表情]' || !message.text)
      && !(message.images && message.images.length))
    // type 8/77/105 video card with no title/cover/link — need payload dump to improve parser
    || ([8, 77, 105].includes(message.messageType)
      && String(message.text || '').trim() === '[视频]'
      && !(message.images && message.images.length)
      && !String(message.link || '').trim())
    // type 7 reply that only shows placeholder text — dump for body recovery
    || (message.messageType === 7
      && /^(?:\[表情\]|\[图片\]|\[回复\])$/.test(String(message.text || '').trim())
      && !(message.images && message.images.length))
  );
  if (
    message.text === '[暂不支持的消息]'
    || isPlaceholderMedia
    || ![5, 7, 8, 17, 27, 77, 50002, 70002].includes(message.messageType)
    || (isPrivate && message.messageType === 7 && !message.quotedText)
  ) {
    log(
      `Douyin IM diagnostic type=${message.messageType}`
      + ` content_keys=${message.contentKeys?.join(',') || '-'}`
      + ` parse=${message.contentParseOk === false ? 'fail' : 'ok'}`
      + ` len=${message.contentLen ?? 0}`
      + ` quote=${JSON.stringify({
        name: message.quotedName || '',
        text: String(message.quotedText || '').slice(0, 80),
        uid: message.quotedSenderUid || '',
        sid: message.quotedServerMessageId || '',
        cid: message.quotedClientMessageId || '',
      })}`
      + ` fields=${(message.fieldNumbers || []).join(',') || '-'}`
      + ` hits=${(message.fieldHits || []).join('|') || '-'}`
      + ` preview=${JSON.stringify(String(message.contentPreview || '').slice(0, 240))}`
      + ` hex=${message.contentHex || '-'}`
      + ` ext=${JSON.stringify(message.extSummary || {}).slice(0, 500)}`,
    );
  }
  if (isPrivate || isTargetGroup) {
    log(
      `Douyin IM publish type=${message.messageType} convType=${message.conversationType}`
      + ` conv=${message.conversationId || '-'}`
      + ` short=${message.conversationShortId || '-'}`
      + ` selfChat=${message.isSelfChat ? 1 : 0}`
      + ` sender=${message.senderUid} name=${senderName || '-'} nick=${senderNickname || '-'} remark=${senderRemark || '-'}`
      + ` text=${String(message.text || '').slice(0, 80)}`
      + ` images=${Array.isArray(message.images) ? message.images.length : 0}`
      + ` link=${String(message.link || '').slice(0, 80)}`
      + (hasQuoteRef ? ` quoted=${JSON.stringify({
        name: message.quotedName || '',
        text: String(message.quotedText || '').slice(0, 60),
        uid: message.quotedSenderUid || '',
      })}` : ''),
    );
  }
  emit('douyin_im_message', {
    ...message,
    selfUid: douyinIMIdentity.selfUid,
    senderName,
    senderNickname,
    senderRemark,
    receivedAt: Date.now(),
  });
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
    let healthTimer;
    let alive = true;
    socket.on('open', () => {
      emit('douyin_im_status', { status: 'connected', message: '抖音私信/群聊只读连接已建立' });
      clearInterval(douyinIMHealthTimer);
      healthTimer = setInterval(() => {
        if (douyinIMSocket !== socket || socket.readyState !== WebSocket.OPEN) return;
        if (!alive) {
          socket.terminate();
          return;
        }
        alive = false;
        try { socket.ping(); } catch { socket.terminate(); }
      }, 30_000);
      healthTimer.unref?.();
      douyinIMHealthTimer = healthTimer;
    });
    socket.on('pong', () => {
      alive = true;
      emit('douyin_im_status', { status: 'connected', message: '抖音私信/群聊只读连接心跳正常' });
    });
    socket.on('message', (raw) => {
      alive = true;
      try {
        const message = decodeDouyinIMPush(raw);
        if (message) void publishDouyinIMMessage(message);
      } catch (error) {
        log(`Douyin IM frame decode failed: ${error.message}`);
      }
    });
    socket.on('error', (error) => emit('douyin_im_status', { status: 'error', message: error.message }));
    socket.on('close', () => {
      clearInterval(healthTimer);
      if (douyinIMHealthTimer === healthTimer) douyinIMHealthTimer = undefined;
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
  if (!settings.douyinIMEnabled || shuttingDown || douyinIMInitRunning) return;
  if (douyinIMIdentity.selfUid) {
    // Re-publish group identity whenever we short-circuit init (restore path /
    // reconnect after Go process restart without sidecar restart).
    emitDouyinIMGroupIdentity();
    await connectDouyinIM();
    return;
  }
  douyinIMInitRunning = true;
  clearTimeout(douyinIMInitRetryTimer);
  douyinIMInitRetryTimer = undefined;
  let targetPage;
  const targetGroupName = String(settings.douyinIMGroupName || '').trim();
  const targetGroupNumber = String(settings.douyinIMGroupNumber || '').trim();
  let initSeen = false;
  let initReady = false;
  const onResponse = async (response) => {
    const responseURL = response.url();
    const isLegacyInit = responseURL.includes('get_message_by_init');
    const isIMAPI = responseURL.includes('imapi.douyin.com');
    if (!isLegacyInit && !isIMAPI) return;
    try {
      const decoded = decodeDouyinIMInit(await response.body());
      if (!decoded.selfUid || decoded.groups.length === 0) return;
      initSeen = true;
      initReady = true;
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
        groupNumber: group?.groupNumber || targetGroupNumber,
      };
      await persistStorageState();
      await persistDouyinIMIdentity();
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
  try {
    targetPage = await getDouyinIMPage();
    let loggedIn = await douyinBrowserLoggedIn();
    if (!loggedIn) {
      // Need scan — open QR so user can open panel browser and scan immediately.
      emit('douyin_status', { status: 'login_required', message: '抖音浏览器需要登录' });
      emit('douyin_im_status', {
        status: 'disconnected',
        message: '抖音未登录，已请求扫码；请打开面板浏览器扫码后自动重连 IM',
      });
      try {
        await requestDouyinLoginQRCode();
      } catch (error) {
        log(`Douyin IM init QR failed: ${error.message}`);
      }
      loggedIn = await douyinBrowserLoggedIn();
      if (!loggedIn) {
        scheduleDouyinIMInitRetry();
        return;
      }
    } else {
      emit('douyin_status', {
        status: 'healthy',
        message: '抖音浏览器已登录',
      });
    }
    context.on('response', onResponse);
    await targetPage.goto('https://www.douyin.com/chat', { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await targetPage.waitForTimeout(12_000);
    // Login interstitial on /chat — treat as need re-login.
    const chatURL = (() => { try { return targetPage.url(); } catch { return ''; } })();
    if (/passport|login|sso/i.test(chatURL) && !/\/chat/i.test(chatURL)) {
      emit('douyin_status', { status: 'login_required', message: '抖音 IM 页跳转登录' });
      emit('douyin_im_status', {
        status: 'disconnected',
        message: `IM 页未登录（${chatURL}），已请求扫码`,
      });
      try { await requestDouyinLoginQRCode(); } catch {}
      scheduleDouyinIMInitRetry();
      return;
    }
    if (!initSeen) {
      emit('douyin_im_status', {
        status: 'init_missing',
        message: `抖音网页 IM 初始化接口未出现，60 秒后自动探测；当前页面：${chatURL || targetPage.url()}`,
      });
    }
  } catch (error) {
    emit('douyin_im_status', { status: 'error', message: `群聊初始化失败：${error.message}` });
  } finally {
    context?.off('response', onResponse);
    douyinIMInitRunning = false;
    if (!initReady) scheduleDouyinIMInitRetry();
  }
}

async function douyinPageSnapshot(targetPage, secUserId) {
  return targetPage.evaluate((fallbackSecUserId) => {
    const postSelector = 'a[href*="/video/"],a[href*="/note/"]';
    const roots = [
      document.querySelector('[data-e2e="user-post-list"]'),
      document.querySelector('[data-e2e="user-post-list-container"]'),
    ].filter(Boolean);
    const root = roots.find((candidate) => candidate.querySelector(postSelector));
    const cards = [];
    const seen = new Set();
    for (const anchor of root?.querySelectorAll(postSelector) || []) {
      const match = anchor.href.match(/\/(video|note)\/(\d+)/);
      if (!match || seen.has(match[2])) continue;
      seen.add(match[2]);
      const image = anchor.querySelector('img');
      cards.push({
        id: match[2], secUserId: fallbackSecUserId, nickname: '',
        desc: anchor.getAttribute('aria-label') || anchor.getAttribute('title') || image?.alt || anchor.textContent?.trim() || '',
        createTime: Number(BigInt(match[2]) >> 32n), type: match[1], url: anchor.href,
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

function emitDouyinAccountState(account, secUserId, profileUrl, posts, profileLive, snapshot = {}) {
  const nickname = profileLive.nickname || posts[0]?.nickname || snapshot.nickname || account.name || '';
  const discoveredLiveId = (profileLive.active || snapshot.liveActive)
    ? (profileLive.liveId || snapshot.liveId || '')
    : '';
  const liveId = discoveredLiveId || account.liveId || '';
  emit('douyin_account', { secUserId, profileUrl, nickname, liveId });
  if (posts.length > 0) emit('douyin_posts', { secUserId, nickname, posts });
}

const DOUYIN_HTTP_UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36';

/** Cookie header for douyin.com from live Playwright context, else storage-state file. */
async function getDouyinCookieHeader() {
  try {
    if (context) {
      const cookies = await context.cookies('https://www.douyin.com');
      if (cookies.length > 0) {
        return cookies.map((c) => `${c.name}=${c.value}`).join('; ');
      }
    }
  } catch {}
  try {
    if (statePath) {
      const saved = JSON.parse(await fs.readFile(statePath, 'utf8'));
      const cookies = (saved.cookies || []).filter((c) => String(c.domain || '').includes('douyin.com'));
      if (cookies.length > 0) {
        return cookies.map((c) => `${c.name}=${c.value}`).join('; ');
      }
    }
  } catch {}
  return '';
}

/**
 * Works path: pure Cookie + HTTP (no profile navigation).
 * Browser is only needed for login / Cookie refresh / IM.
 */
async function fetchAwemePostsHTTP(secUserId, cookieHeader) {
  if (!cookieHeader) return { posts: [], status_code: -1, message: 'empty cookie' };
  const params = new URLSearchParams({
    device_platform: 'webapp',
    aid: '6383',
    channel: 'channel_pc_web',
    sec_user_id: secUserId,
    count: '18',
    max_cursor: '0',
    publish_video_strategy_type: '2',
    personal_center_strategy: '1',
  });
  const url = `https://www.douyin.com/aweme/v1/web/aweme/post/?${params.toString()}`;
  const response = await fetch(url, {
    headers: {
      cookie: cookieHeader,
      'user-agent': DOUYIN_HTTP_UA,
      referer: 'https://www.douyin.com/',
      accept: 'application/json, text/plain, */*',
    },
  });
  const body = await response.json().catch(() => null);
  const status_code = Number(body?.status_code);
  const posts = status_code === 0 ? normalizeAwemeList(body, secUserId) : [];
  return {
    posts,
    status_code: Number.isFinite(status_code) ? status_code : -1,
    message: String(body?.status_msg || body?.message || ''),
    http: response.status,
  };
}

async function scanDouyinAccount(account, cookieHeader = '') {
  const secUserId = String(account.secUserId || '').trim();
  if (!secUserId) return;
  const profileUrl = account.profileUrl || `https://www.douyin.com/user/${encodeURIComponent(secUserId)}`;
  const nameHint = account.name || secUserId.slice(0, 12);

  try {
    const cookie = cookieHeader || await getDouyinCookieHeader();
    if (!cookie) {
      emit('douyin_account_error', { secUserId, message: '抖音 Cookie 为空，请先在面板登录' });
      emit('douyin_account', { secUserId, profileUrl, nickname: account.name || '', liveId: account.liveId || '' });
      return;
    }

    const result = await fetchAwemePostsHTTP(secUserId, cookie);
    if (result.posts.length > 0) {
      // Success path: no browser profile navigation.
      emitDouyinAccountState(account, secUserId, profileUrl, result.posts, { active: false, liveId: '', nickname: '' }, {});
      return;
    }

    // Soft failures: empty list with status 0 may mean private/no posts; treat as empty success.
    if (result.status_code === 0) {
      emit('douyin_account', {
        secUserId,
        profileUrl,
        nickname: account.name || '',
        liveId: account.liveId || '',
      });
      return;
    }

    // Auth / risk — do NOT thrash profile pages; surface once.
    const msg = result.message || `status_code=${result.status_code} http=${result.http}`;
    log(`douyin HTTP posts empty for ${nameHint}: ${msg}`);
    if (/验证|captcha|login|登录|未登录|risk|风控/i.test(msg) || result.status_code === 8 || result.http === 403) {
      emit('douyin_account_error', {
        secUserId,
        message: `抖音作品 HTTP 拉取失败：${msg || '可能需要重新登录或过验证'}`,
      });
    } else {
      emit('douyin_account_error', {
        secUserId,
        message: `抖音作品 HTTP 拉取失败：${msg || 'empty'}`,
      });
    }
    emit('douyin_account', {
      secUserId,
      profileUrl,
      nickname: account.name || '',
      liveId: account.liveId || '',
    });
  } catch (error) {
    emit('douyin_account_error', { secUserId, message: error.message });
  }
}

function mergeProfileScanQueue(next) {
  if (!next) return;
  if (!profileScanQueued || profileScanQueued === next) {
    profileScanQueued = next;
    return;
  }
  profileScanQueued = 'both';
}

async function runProfileScans(which) {
  if (shuttingDown) return;
  if (profileScanRunning) {
    mergeProfileScanQueue(which);
    return;
  }
  profileScanRunning = true;
  let pending = which || 'both';
  try {
    while (!shuttingDown && pending) {
      const doDouyin = pending === 'douyin' || pending === 'both';
      const doXhs = pending === 'xiaohongshu' || pending === 'both';
      pending = null;
      if (doDouyin) await scanAllDouyinInner();
      if (doXhs) await scanAllXiaohongshuInner();
      // Drain coalesced requests that arrived while we were scanning.
      pending = profileScanQueued;
      profileScanQueued = null;
    }
  } finally {
    profileScanRunning = false;
  }
}

async function scanAllDouyinInner() {
  if (shuttingDown || !settings.douyinEnabled || settings.douyinAccounts.length === 0) return;
  if (douyinScanning) return;
  douyinScanning = true;
  try {
    // Works path is Cookie+HTTP only — no profile page.goto.
    // Cookie from live Playwright context (IM already up) or weibo-storage-state.json.
    let cookie = await getDouyinCookieHeader();
    if (!cookie) {
      // Cold start: open browser profile once to load cookies, still no profile navigation.
      await startBrowser();
      cookie = await getDouyinCookieHeader();
    }
    log(`douyin works scan via HTTP accounts=${settings.douyinAccounts.length} cookie=${cookie ? 'yes' : 'no'}`);
    for (const account of settings.douyinAccounts) {
      if (shuttingDown) break;
      await scanDouyinAccount(account, cookie);
    }
  } finally {
    douyinScanning = false;
  }
}

async function scanAllDouyin() {
  return runProfileScans('douyin');
}

function scheduleDouyin() {
  clearInterval(douyinTimer);
  // NOTE: do NOT clear xiaohongshuTimer here — that was a bug that stopped
  // Xiaohongshu polling whenever Douyin schedule was refreshed.
  if (!settings.douyinEnabled) return;
  const seconds = Math.max(15, Number(settings.douyinPollSeconds) || 60);
  douyinTimer = setInterval(() => void scanAllDouyin(), seconds * 1000);
}

async function douyinBrowserLoggedIn() {
  await startBrowser();
  const cookies = new Map((await context.cookies()).map((cookie) => [cookie.name, cookie.value]));
  return cookies.get('LOGIN_STATUS') === '1' || Boolean(cookies.get('sessionid') || cookies.get('sessionid_ss'));
}

async function requestDouyinLoginQRCode() {
  const targetPage = await getDouyinPage();
  await targetPage.goto('https://www.douyin.com/', { waitUntil: 'domcontentloaded', timeout: 45_000 });
  if (await douyinBrowserLoggedIn()) {
    emit('douyin_status', { status: 'healthy', message: '抖音浏览器已登录' });
    return;
  }
  let cookies = new Map((await context.cookies()).map((cookie) => [cookie.name, cookie.value]));
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
      // Works/IM no longer need this tab; free a renderer.
      await releaseDouyinPage('login-ok');
      void scanAllDouyin();
      void startDouyinIM();
      return;
    }
  }
  emit('douyin_status', { status: 'qrcode_expired', message: '抖音登录二维码已过期' });
  await releaseDouyinPage('login-expired');
}

async function xiaohongshuLoggedIn() {
  const health = await xiaohongshuSessionHealthy();
  return Boolean(health.ok);
}

/** Cookie present only — NOT a guarantee notes work. Panel must not treat this as green. */
async function xiaohongshuCookiePresent() {
  const cookies = await readXiaohongshuCookieMap();
  return Boolean(cookies.get('a1') && cookies.get('web_session'));
}

/**
 * Xiaohongshu notes via page-context signed fetch (no profile goto).
 *
 * Pure Node HTTP is blocked: real X-s is XYS_ from seccore_signv2 + window.mnsv2
 * (server-delivered VM bytecode via /api/sec/v1/scripting). X-S-Common is
 * custom-alphabet b64 JSON; x-rap-param comes from RAP anti_hp interceptors.
 *
 * Strategy (2026-07-19 probe):
 *  1) Cache X-S-Common + x-rap-param from the browser's own user_posted XHR
 *     (captured during profile fallback / first successful native call).
 *  2) Each poll: page stays on explore; call mnsv2 → pack XYS_ (x2=xsecplatform)
 *     + attach cached common/rap → fetch edith user_posted.
 *  3) If notes empty / 300011, fall back to profile goto (which also refreshes cache).
 */
const XHS_CUSTOM_B64 = 'ZmserbBoHQtNP+wOcza/LpngG8yJq42KWYj0DSfdikx3VT16IlUAFM97hECvuRX5';
/** @type {{ xsCommon: string, rap: string, capturedAt: number, source: string }} */
let xiaohongshuSignCache = { xsCommon: '', rap: '', capturedAt: 0, source: '' };

function rememberXiaohongshuSignHeaders(headers, source = 'native') {
  if (!headers || typeof headers !== 'object') return;
  const common = headers['x-s-common'] || headers['X-S-Common'] || headers['X-s-common'] || '';
  const rap = headers['x-rap-param'] || headers['x-rap-param'.toUpperCase()] || headers['X-Rap-Param'] || '';
  const xs = headers['x-s'] || headers['X-s'] || '';
  const commonStr = String(common || '');
  // Real X-S-Common is ~2.5KB. Short fragments (hundreds of chars) from intermediate
  // edith calls must NOT overwrite a good cache — prod 2026-07-20 saw 328/424 clobber 2528 → 461.
  if (commonStr.length < 800) {
    if (commonStr.length > 20) {
      // keep rap if useful, but don't demote common
      if (rap && String(rap).length > 20 && !xiaohongshuSignCache.rap) {
        xiaohongshuSignCache.rap = String(rap);
        xiaohongshuSignCache.capturedAt = Date.now();
      }
    }
    return;
  }
  // Prefer longer (more complete) common; never replace a longer cache with a shorter one.
  if (xiaohongshuSignCache.xsCommon && commonStr.length < xiaohongshuSignCache.xsCommon.length) {
    if (rap && String(rap).length > (xiaohongshuSignCache.rap?.length || 0)) {
      xiaohongshuSignCache.rap = String(rap);
      xiaohongshuSignCache.capturedAt = Date.now();
    }
    return;
  }
  xiaohongshuSignCache = {
    xsCommon: commonStr,
    rap: String(rap || xiaohongshuSignCache.rap || ''),
    capturedAt: Date.now(),
    source: `${source}:xs=${String(xs).slice(0, 8)}`,
  };
  log(`xiaohongshu sign-cache updated source=${source} commonLen=${commonStr.length} rapLen=${String(rap).length}`);
}

function attachXiaohongshuSignCapture(targetPage) {
  if (!targetPage || targetPage.__xhsSignCapture) return;
  targetPage.__xhsSignCapture = true;
  // Capture X-S-Common / x-rap-param from ANY edith signed call (not only user_posted).
  // Do NOT rely on per-account profile goto as the notes fallback path.
  targetPage.on('request', (req) => {
    try {
      const url = req.url();
      if (!/edith\.xiaohongshu\.com|xiaohongshu\.com\/api\//i.test(url)) return;
      rememberXiaohongshuSignHeaders(req.headers(), 'page-request');
    } catch {}
  });
}

async function ensureXiaohongshuSignPage() {
  const p = await getXiaohongshuPage();
  attachXiaohongshuSignCapture(p);
  let url = '';
  try { url = p.url(); } catch { url = ''; }

  // Login dead: NEVER navigate/refresh xhs pages — only reuse current tab for API if possible.
  if (xiaohongshuLoginDead) {
    log(`xiaohongshu ensureSignPage: login-dead, no navigate (${xiaohongshuLoginDeadReason || 'login_required'}) url=${String(url).slice(0, 100)}`);
    return p;
  }

  // Already on login/captcha page: do not steal the page for explore warm-up.
  if (/\/login|website-login|captcha|verify|qrcode/i.test(url)) {
    log(`xiaohongshu ensureSignPage: login/captcha page open, no navigate url=${url.slice(0, 120)}`);
    return p;
  }

  const okHost = /xiaohongshu\.com/i.test(url) && !/website-login\/error|error_code=300012|\/login/i.test(url);
  // Keep ONE explore (or already-open xhs) tab for mnsv2 + cookie. Never profile-hop for notes.
  if (!okHost || url === 'about:blank' || !url) {
    await p.goto('https://www.xiaohongshu.com/explore', { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await applyXiaohongshuLocalStorage(p);
    await p.waitForTimeout(1_500);
  }
  // Wait for mnsv2 (real XYS_ path). _webmsxyw alone only yields XYW_ which is not enough.
  await waitUntil(async () => {
    try {
      return await p.evaluate(() => typeof window.mnsv2 === 'function' || typeof window._webmsxyw === 'function'
        || /xiaohongshu\.com/i.test(location.hostname));
    } catch { return false; }
  }, { timeoutMs: 8_000, intervalMs: 250 }).catch(() => {});
  // Stale sign cache is a common cause of HTTP 461 + empty notes while code/msg still look "成功".
  // Drop headers older than 45s so the next edith traffic refreshes X-S-Common / rap.
  if (xiaohongshuSignCache.xsCommon && xiaohongshuSignCache.capturedAt
      && Date.now() - xiaohongshuSignCache.capturedAt > 45_000) {
    log(`xiaohongshu sign-cache stale ageMs=${Date.now() - xiaohongshuSignCache.capturedAt} → clear`);
    xiaohongshuSignCache = { xsCommon: '', rap: '', capturedAt: 0, source: '' };
  }
  // Cold start / after clear: wait briefly for explore's own edith traffic to fill X-S-Common.
  // This is NOT a per-account profile goto fallback — only seeds headers on the kept explore tab.
  if (!xiaohongshuSignCache.xsCommon) {
    await waitUntil(async () => Boolean(xiaohongshuSignCache.xsCommon), {
      timeoutMs: 4_000,
      intervalMs: 200,
    }).catch(() => {});
    if (!xiaohongshuSignCache.xsCommon) {
      // Nudge explore once so SPA fires signed APIs (feed/homefeed), still no user profile goto.
      try {
        await p.evaluate(() => window.scrollBy(0, 400));
        await p.waitForTimeout(800);
      } catch {}
      await waitUntil(async () => Boolean(xiaohongshuSignCache.xsCommon), {
        timeoutMs: 3_000,
        intervalMs: 200,
      }).catch(() => {});
    }
  }
  return p;
}

/** Force re-warm explore + clear cached signs. Rate-limited — thrashing explore was
 *  wiping login/captcha and worsening risk (user 2026-07-20). No profile goto. */
let xiaohongshuLastSignEnvRefreshAt = 0;
async function refreshXiaohongshuSignEnv(reason = '') {
  // Hard stop when login is dead: never refresh/navigate xhs pages.
  if (xiaohongshuLoginDead) {
    log(`xiaohongshu sign-env refresh blocked (login-dead): ${reason || 'unspecified'}`);
    return false;
  }
  const now = Date.now();
  // At most once per 10 minutes.
  if (now - xiaohongshuLastSignEnvRefreshAt < 10 * 60_000) {
    log(`xiaohongshu sign-env refresh skipped (cooldown): ${reason || 'unspecified'}`);
    return false;
  }
  xiaohongshuLastSignEnvRefreshAt = now;
  log(`xiaohongshu sign-env refresh: ${reason || 'unspecified'}`);
  xiaohongshuSignCache = { xsCommon: '', rap: '', capturedAt: 0, source: '' };
  try {
    const p = await getXiaohongshuPage();
    // If user is mid login/captcha, do NOT navigate away.
    const cur = (() => { try { return p.url(); } catch { return ''; } })();
    if (/\/login|website-login|captcha|verify|qrcode/i.test(cur)) {
      log(`xiaohongshu sign-env refresh aborted (login page open): ${cur.slice(0, 120)}`);
      return false;
    }
    attachXiaohongshuSignCapture(p);
    await p.goto('https://www.xiaohongshu.com/explore', { waitUntil: 'domcontentloaded', timeout: 45_000 });
    await applyXiaohongshuLocalStorage(p);
    await p.waitForTimeout(1_200);
    try {
      await p.evaluate(() => {
        window.scrollBy(0, 500);
        window.scrollBy(0, -200);
      });
    } catch {}
    await waitUntil(async () => {
      try {
        return await p.evaluate(() => typeof window.mnsv2 === 'function');
      } catch { return false; }
    }, { timeoutMs: 6_000, intervalMs: 200 }).catch(() => {});
    await waitUntil(async () => Boolean(xiaohongshuSignCache.xsCommon), {
      timeoutMs: 5_000,
      intervalMs: 200,
    }).catch(() => {});
    log(`xiaohongshu sign-env refresh done commonLen=${xiaohongshuSignCache.xsCommon?.length || 0} rapLen=${xiaohongshuSignCache.rap?.length || 0}`);
    return true;
  } catch (error) {
    log(`xiaohongshu sign-env refresh failed: ${error.message}`);
    return false;
  }
}

function isXiaohongshuSignishMiss(result) {
  if (!result || result.ok) return false;
  const status = Number(result.status || 0);
  const code = result.code;
  const msg = String(result.msg || '');
  const notesLen = Array.isArray(result.notes) ? result.notes.length : 0;
  // Account abnormal / login / IP risk: NOT fixed by re-warming explore.
  if (
    code === 300011 || code === '300011' || code === 300012 || code === '300012'
    || code === -100 || code === '-100'
    || /Account abnormal|登录|login|IP存在风险|安全限制|300011|300012/i.test(`${msg} ${code}`)
  ) {
    return false;
  }
  // HTTP 461 alone is NOT always "stale sign" — can be risk wall.
  if (status === 461) return true;
  if (status >= 400 && status !== 401 && status !== 403 && notesLen === 0) return true;
  if ((code === 0 || code === '0') && notesLen === 0 && status !== 200) return true;
  if (msg === '成功' && notesLen === 0 && status !== 200) return true;
  return false;
}

/** Detect 300012 IP-risk / security wall. Prefer explicit signals over guessing from 461. */
async function detectXiaohongshuIpRisk(page) {
  try {
    const url = page?.url?.() || '';
    if (/error_code=300012|IP存在风险|website-login\/error/i.test(url)) {
      return { risk: true, reason: `小红书风控/安全限制：IP存在风险（url）` };
    }
  } catch {}
  try {
    const probe = await page.evaluate(async () => {
      // user/me is light and signed by the page itself on explore; guest=true under risk.
      try {
        const me = await fetch('https://edith.xiaohongshu.com/api/sns/web/v2/user/me', {
          credentials: 'include',
          headers: { accept: 'application/json, text/plain, */*', referer: location.href },
        });
        let body = null;
        try { body = await me.json(); } catch {}
        return {
          href: location.href,
          meStatus: me.status,
          meCode: body?.code,
          meMsg: body?.msg,
          guest: body?.data?.guest,
          userId: body?.data?.user_id,
          title: document.title,
          bodyText: (document.body?.innerText || '').replace(/\s+/g, ' ').slice(0, 120),
        };
      } catch (e) {
        return { href: location.href, err: String(e?.message || e), title: document.title };
      }
    });
    if (probe && (
      /300012|IP存在风险|安全限制/i.test(`${probe.href} ${probe.title} ${probe.bodyText} ${probe.meMsg || ''}`)
      || Number(probe.meCode) === 300012
    )) {
      return { risk: true, reason: '小红书风控/安全限制：IP存在风险，请切换可靠网络环境后重试', probe };
    }
    // guest-only under security wall often pairs with 461 on user_posted
    if (probe?.guest === true && /website-login\/error|安全限制/i.test(`${probe.href} ${probe.title}`)) {
      return { risk: true, reason: '小红书风控/安全限制：IP存在风险（guest + 安全页）', probe };
    }
    return { risk: false, probe };
  } catch (e) {
    return { risk: false, err: String(e?.message || e) };
  }
}

async function fetchXiaohongshuUserPosted(page, userId, { cursor = '', num = 30 } = {}) {
  const pathWithQuery = `/api/sns/web/v1/user_posted?num=${encodeURIComponent(String(num))}`
    + `&cursor=${encodeURIComponent(cursor)}`
    + `&user_id=${encodeURIComponent(userId)}`
    + `&image_formats=jpg,webp,avif&xsec_token=&xsec_source=`;
  // Precompute MD5 in Node (page has no md5 / CryptoJS on explore).
  const crypto = await import('node:crypto');
  const md5Path = crypto.createHash('md5').update(pathWithQuery).digest('hex');
  const md5Both = md5Path; // GET body empty
  const cache = {
    xsCommon: xiaohongshuSignCache.xsCommon || '',
    rap: xiaohongshuSignCache.rap || '',
    cacheAgeMs: xiaohongshuSignCache.capturedAt ? Date.now() - xiaohongshuSignCache.capturedAt : -1,
  };
  return page.evaluate(async ({ apiPath, md5Path, md5Both, customAlphabet, cache }) => {
    const url = `https://edith.xiaohongshu.com${apiPath}`;
    function customB64Encode(str) {
      const bytes = new TextEncoder().encode(str);
      let out = '';
      for (let i = 0; i < bytes.length; i += 3) {
        const a = bytes[i];
        const b = i + 1 < bytes.length ? bytes[i + 1] : 0;
        const c = i + 2 < bytes.length ? bytes[i + 2] : 0;
        const n = (a << 16) | (b << 8) | c;
        out += customAlphabet[(n >> 18) & 63];
        out += customAlphabet[(n >> 12) & 63];
        out += i + 1 < bytes.length ? customAlphabet[(n >> 6) & 63] : '=';
        out += i + 2 < bytes.length ? customAlphabet[n & 63] : '=';
      }
      return out;
    }
    function detectSignVersion() {
      // Prefer live page build markers — hardcoding 4.3.7 produced shorter X-s (352 vs native ~360).
      try {
        const m = document.cookie.match(/(?:^|;\s*)webBuild=([^;]+)/);
        if (m && m[1]) return decodeURIComponent(m[1]).trim();
      } catch {}
      try {
        if (window.__XHS_VERSION__) return String(window.__XHS_VERSION__);
      } catch {}
      try {
        const meta = document.querySelector('meta[name="xhs-version"], meta[name="version"]');
        if (meta?.content) return String(meta.content).trim();
      } catch {}
      return '4.3.7';
    }
    function packXYS(x3) {
      // If mnsv2 already returns a full signed header value, use it.
      const raw = x3 == null ? '' : String(x3);
      if (/^XY[SW]_/i.test(raw)) return raw;
      const platform = String(window.xsecplatform || 'Linux');
      const x0 = detectSignVersion();
      // Match native field set/order observed on pc-web.
      const payload = JSON.stringify({
        x0,
        x1: 'xhs-pc-web',
        x2: platform,
        x3: raw,
        x4: '',
      });
      return 'XYS_' + customB64Encode(payload);
    }
    function randomHex(len) {
      const bytes = new Uint8Array(Math.ceil(len / 2));
      crypto.getRandomValues(bytes);
      return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('').slice(0, len);
    }
    function browserishBaseHeaders() {
      // Align with native edith requests (origin/trace). UA/sec-ch-ua usually added by Chromium.
      return {
        accept: 'application/json, text/plain, */*',
        origin: 'https://www.xiaohongshu.com',
        referer: location.href || 'https://www.xiaohongshu.com/',
        'x-b3-traceid': randomHex(16),
        'x-xray-traceid': randomHex(32),
      };
    }
    async function doFetch(headers, signMeta) {
      const res = await fetch(url, {
        method: 'GET',
        credentials: 'include',
        headers: {
          ...browserishBaseHeaders(),
          ...headers,
        },
      });
      let body = null;
      try { body = await res.json(); } catch {
        return { ok: false, status: res.status, code: -1, msg: 'non-json', notes: [], cursor: '', hasMore: false, signMeta };
      }
      const notes = body?.data?.notes || body?.data?.list || body?.notes || [];
      const code = body?.code ?? body?.result ?? res.status;
      const msg = String(body?.msg || body?.message || '');
      const ok = res.status >= 200 && res.status < 300 && (code === 0 || code === '0' || code === 200)
        && Array.isArray(notes) && notes.length > 0;
      return {
        ok,
        status: res.status,
        code,
        msg,
        notes: Array.isArray(notes) ? notes : [],
        cursor: String(body?.data?.cursor || body?.data?.next_cursor || ''),
        hasMore: Boolean(body?.data?.has_more ?? body?.data?.hasMore),
        nickname: String(
          notes?.[0]?.user?.nickname
          || notes?.[0]?.user?.nick_name
          || notes?.[0]?.note_card?.user?.nickname
          || notes?.[0]?.noteCard?.user?.nickname
          || '',
        ),
        signMeta,
      };
    }

    try {
      const pathOnly = apiPath.startsWith('http')
        ? new URL(apiPath).pathname + new URL(apiPath).search
        : apiPath;
      const attempts = [];

      // A) Preferred: mnsv2 → XYS_ + cached X-S-Common (+ rap if present)
      if (typeof window.mnsv2 === 'function') {
        let x3 = null;
        try { x3 = window.mnsv2(pathOnly, md5Both, md5Path); } catch (e) {
          attempts.push({ mode: 'mnsv2-err', err: String(e?.message || e) });
        }
        if (x3 != null) {
          const xt = Date.now();
          const xs = packXYS(x3);
          const headers = { 'X-s': xs, 'X-t': String(xt) };
          if (cache.xsCommon) headers['X-S-Common'] = cache.xsCommon;
          if (cache.rap) headers['x-rap-param'] = cache.rap;
          const signMeta = {
            mode: cache.xsCommon ? 'mnsv2+cacheCommon' : 'mnsv2-only',
            xsPrefix: String(xs).slice(0, 4),
            xsLen: String(xs).length,
            x0: detectSignVersion(),
            platform: String(window.xsecplatform || ''),
            xt: String(xt),
            commonLen: cache.xsCommon ? cache.xsCommon.length : 0,
            rapLen: cache.rap ? cache.rap.length : 0,
            cacheAgeMs: cache.cacheAgeMs,
          };
          const r = await doFetch(headers, signMeta);
          if (r.ok) return r;
          attempts.push({ ...signMeta, code: r.code, msg: r.msg, status: r.status, notes: r.notes?.length || 0 });
          // keep last failure as return if nothing better
          if (!attempts._lastFail) attempts._lastFail = r;
          attempts._lastFail = r;
        }
      }

      // B) _webmsxyw (XYW_) + cached common — usually 300011 but cheap
      if (typeof window._webmsxyw === 'function') {
        try {
          const xy = window._webmsxyw(pathOnly, undefined) || window._webmsxyw(pathOnly, '');
          if (xy && (xy['X-s'] || xy['x-s'])) {
            const headers = {
              'X-s': xy['X-s'] || xy['x-s'],
              'X-t': String(xy['X-t'] || xy['x-t'] || Date.now()),
            };
            if (cache.xsCommon) headers['X-S-Common'] = cache.xsCommon;
            if (cache.rap) headers['x-rap-param'] = cache.rap;
            const signMeta = {
              mode: 'webmsxyw+cache',
              xsPrefix: String(headers['X-s']).slice(0, 4),
              xsLen: String(headers['X-s']).length,
              xt: headers['X-t'],
              commonLen: cache.xsCommon ? cache.xsCommon.length : 0,
            };
            const r = await doFetch(headers, signMeta);
            if (r.ok) return r;
            attempts.push({ ...signMeta, code: r.code, msg: r.msg, notes: r.notes?.length || 0 });
            attempts._lastFail = r;
          }
        } catch (e) {
          attempts.push({ mode: 'webmsxyw-err', err: String(e?.message || e) });
        }
      }

      // C) site axios if present (interceptors inject full signs)
      if (window.axios && typeof window.axios.get === 'function') {
        try {
          const ax = await window.axios.get(url, { withCredentials: true });
          const body = ax.data;
          const notes = body?.data?.notes || body?.data?.list || [];
          const code = body?.code ?? ax.status;
          const ok = (code === 0 || code === '0') && Array.isArray(notes) && notes.length > 0;
          const r = {
            ok,
            status: ax.status || 200,
            code,
            msg: String(body?.msg || ''),
            notes: Array.isArray(notes) ? notes : [],
            cursor: String(body?.data?.cursor || ''),
            hasMore: Boolean(body?.data?.has_more),
            nickname: String(notes?.[0]?.user?.nickname || ''),
            signMeta: { mode: 'axios' },
          };
          if (r.ok) return r;
          attempts._lastFail = r;
        } catch (axErr) {
          attempts.push({ mode: 'axios-err', err: String(axErr?.message || axErr) });
        }
      }

      const fail = attempts._lastFail || {
        ok: false, status: 0, code: -1, msg: 'no-sign-path', notes: [], cursor: '', hasMore: false,
        signMeta: { mode: 'none', attempts },
      };
      fail.signMeta = { ...(fail.signMeta || {}), attempts: attempts.filter((a) => !a || typeof a === 'object') };
      return fail;
    } catch (e) {
      return { ok: false, status: 0, code: -1, msg: String(e?.message || e), notes: [], cursor: '', hasMore: false };
    }
  }, {
    apiPath: pathWithQuery,
    md5Path,
    md5Both,
    customAlphabet: XHS_CUSTOM_B64,
    cache,
  });
}

async function scanXiaohongshuAccountViaAPI(account) {
  const userId = String(account.userId || '').trim();
  if (!userId) return { ok: false, reason: 'empty userId' };

  // Login dead: do not navigate; optionally try pure API only when cookies still present.
  // IMPORTANT: do NOT keep hammering every poll with opaque 461/"成功" — that re-marks
  // login-dead with misleading reasons and can disrupt an in-progress QR login.
  if (xiaohongshuLoginDead) {
    const cookies = await readXiaohongshuCookieMap();
    const hasCookie = Boolean(cookies.get('a1') && cookies.get('web_session'));
    const p = await getXiaohongshuPage();
    const url = pageUrlSafe(p);
    const onXhs = /xiaohongshu\.com/i.test(url) && !/\/login|website-login|about:blank/i.test(url);

    // Soft recovery path: cookies + normal page → probe user/me once.
    if (hasCookie && onXhs) {
      try {
        const me = await p.evaluate(async () => {
          try {
            const res = await fetch('https://edith.xiaohongshu.com/api/sns/web/v2/user/me', {
              credentials: 'include',
              headers: {
                accept: 'application/json, text/plain, */*',
                origin: 'https://www.xiaohongshu.com',
                referer: location.href || 'https://www.xiaohongshu.com/',
              },
            });
            const body = await res.json().catch(() => ({}));
            return {
              httpStatus: res.status,
              code: body?.code,
              msg: body?.msg,
              guest: body?.data?.guest,
              userId: body?.data?.user_id,
              nickname: body?.data?.nickname,
            };
          } catch (e) {
            return { err: String(e?.message || e) };
          }
        });
        log(`xiaohongshu login-dead me-probe: ${JSON.stringify(me)}`);
        const meCode = Number(me?.code);
        const stillGuest = me?.guest === true
          || meCode === -100
          || meCode === -101
          || /登录已过期|无登录信息/i.test(String(me?.msg || ''));
        if (!stillGuest && me?.guest === false && me?.userId) {
          clearXiaohongshuLoginDead('me ok while login-dead');
          // fall through to normal API path below (do not return early)
        } else {
          return {
            ok: false,
            reason: `小红书需重新登录（login-dead，me 未恢复）：${xiaohongshuLoginDeadReason || me?.msg || 'login_required'}`,
            blocked: true,
            loginDead: true,
            me,
          };
        }
      } catch (e) {
        log(`xiaohongshu login-dead me-probe failed: ${e.message}`);
        return {
          ok: false,
          reason: `小红书需重新登录（login-dead）：${xiaohongshuLoginDeadReason || 'login_required'}`,
          blocked: true,
          loginDead: true,
        };
      }
    } else {
      // No usable cookies / page — do not call user_posted at all.
      return {
        ok: false,
        reason: `小红书需重新登录（login-dead，禁止刷页/打帖）：${xiaohongshuLoginDeadReason || 'login_required'} cookie=${hasCookie} url=${String(url).slice(0, 80)}`,
        blocked: true,
        loginDead: true,
      };
    }
  }

  let page = await ensureXiaohongshuSignPage();
  let afterUrl = (() => { try { return page.url(); } catch { return ''; } })();
  if (/website-login\/error|error_code=300012/i.test(afterUrl)) {
    return {
      ok: false,
      reason: '小红书风控/安全限制：IP存在风险，请切换可靠网络环境后重试',
      blocked: true,
      risk: true,
    };
  }
  if (/\/login/i.test(afterUrl) && !/user\/profile\//i.test(afterUrl)) {
    markXiaohongshuLoginDead('on login page during scan');
    return { ok: false, reason: '小红书需重新登录，笔记列表暂不可用', blocked: true, loginDead: true };
  }

  // Preflight: 300012 IP risk makes user_posted return opaque HTTP 461 + empty success.
  // Detect clearly and stop thrashing sign-refresh / sidecar restart.
  const riskCheck = await detectXiaohongshuIpRisk(page);
  if (riskCheck.risk) {
    log(`xiaohongshu IP risk preflight: ${riskCheck.reason} probe=${JSON.stringify(riskCheck.probe || {})}`);
    return { ok: false, reason: riskCheck.reason, blocked: true, risk: true };
  }

  // Guest gate: if user/me is guest, do NOT thrash user_posted (signature won't fix session).
  try {
    const meGate = await page.evaluate(async () => {
      try {
        const res = await fetch('https://edith.xiaohongshu.com/api/sns/web/v2/user/me', {
          credentials: 'include',
          headers: {
            accept: 'application/json, text/plain, */*',
            origin: 'https://www.xiaohongshu.com',
            referer: location.href || 'https://www.xiaohongshu.com/',
          },
        });
        const body = await res.json().catch(() => ({}));
        return {
          httpStatus: res.status,
          code: body?.code,
          msg: body?.msg,
          guest: body?.data?.guest,
          userId: body?.data?.user_id,
          nickname: body?.data?.nickname,
        };
      } catch (e) {
        return { err: String(e?.message || e) };
      }
    });
    const meCode = Number(meGate?.code);
    const isGuest = meGate?.guest === true
      || meCode === -100
      || meCode === -101
      || /登录已过期|无登录信息/i.test(String(meGate?.msg || ''));
    if (isGuest) {
      const reason = meGate?.msg
        ? `小红书会话为 guest/未登录（user/me code=${meGate.code} msg=${meGate.msg}），跳过 user_posted`
        : '小红书会话为 guest/未登录，跳过 user_posted';
      log(`xiaohongshu guest-gate: ${JSON.stringify(meGate)}`);
      markXiaohongshuLoginDead(reason);
      return { ok: false, reason, blocked: true, loginDead: true, me: meGate };
    }
    if (meGate?.guest === false && meGate?.userId) {
      clearXiaohongshuLoginDead('guest-gate me ok');
    }
  } catch (e) {
    log(`xiaohongshu guest-gate failed: ${e.message}`);
  }

  let result = await fetchXiaohongshuUserPosted(page, userId, { num: 30 });
  // Fast auto-heal only for true sign-env staleness — never for login-dead / account abnormal.
  if (!result.ok && isXiaohongshuSignishMiss(result) && !xiaohongshuLoginDead) {
    // Re-check risk after first 461: user_posted often hides 300012 as empty success.
    const riskAfter = await detectXiaohongshuIpRisk(page);
    if (riskAfter.risk) {
      log(`xiaohongshu 461 mapped to IP risk: ${riskAfter.reason}`);
      return { ok: false, reason: riskAfter.reason, blocked: true, risk: true, result };
    }
    log(`xiaohongshu signish miss user=${account.name || userId} status=${result.status} code=${result.code} notes=${result.notes?.length || 0} → refresh+retry`);
    await refreshXiaohongshuSignEnv(`api-miss status=${result.status} code=${result.code}`);
    page = await ensureXiaohongshuSignPage();
    afterUrl = (() => { try { return page.url(); } catch { return ''; } })();
    if (/website-login\/error|error_code=300012/i.test(afterUrl)) {
      return {
        ok: false,
        reason: '小红书风控/安全限制：IP存在风险，请切换可靠网络环境后重试',
        blocked: true,
        risk: true,
      };
    }
    if (/\/login/i.test(afterUrl) && !/user\/profile\//i.test(afterUrl)) {
      markXiaohongshuLoginDead('redirected to login after sign refresh');
      return { ok: false, reason: '小红书需重新登录，笔记列表暂不可用', blocked: true, loginDead: true };
    }
    result = await fetchXiaohongshuUserPosted(page, userId, { num: 30 });
    if (result.ok) {
      log(`xiaohongshu API recovered after sign-env refresh user=${account.name || userId} notes=${result.notes?.length || 0}`);
    } else if (Number(result.status) === 461) {
      // Second 461 after refresh: treat as risk/network, not infinite sign loop.
      const risk2 = await detectXiaohongshuIpRisk(page);
      const reason = risk2.risk
        ? risk2.reason
        : '小红书 API 持续 461（疑似 IP 风控/网络环境），刷新签名无效';
      log(`xiaohongshu sustained 461 after refresh → stop thrash: ${reason}`);
      return { ok: false, reason, blocked: true, risk: true, result };
    }
  }
  if (!result.ok) {
    const status = Number(result.status || 0);
    const code = Number(result.code);
    const msg = `${result.msg || ''} ${result.code || ''}`;
    // Opaque HTTP 461 with empty "成功"/code0 is NOT "need re-login" — treat as risk/network.
    const opaque461 = status === 461 || (status >= 400 && status < 500 && (code === 0 || Number.isNaN(code)) && /成功|success/i.test(String(result.msg || '')));
    const softLogin = !opaque461 && /登录|login|登录已过期|无登录信息|账号异常|Account abnormal|300011|300012|-100|-101|IP存在风险|安全限制/i.test(msg);
    // True session death: -100/-101 or explicit login text. Exclude 300011/461/IP risk.
    const loginDead = !opaque461
      && (
        code === -100
        || code === -101
        || (
          /登录已过期|无登录信息|未登录|重新登录/i.test(String(result.msg || ''))
          && !/Account abnormal|账号异常|300011|300012|IP存在风险|安全限制/i.test(msg)
        )
      );
    if (loginDead) markXiaohongshuLoginDead(`${result.code} ${result.msg}`);
    return {
      ok: false,
      reason: result.msg || `user_posted status=${result.status} code=${result.code}`,
      blocked: softLogin || opaque461,
      risk: opaque461 || /300012|IP存在风险|安全限制/i.test(msg),
      loginDead,
      result,
    };
  }
  if (!Array.isArray(result.notes) || result.notes.length === 0) {
    return { ok: false, reason: 'user_posted empty notes', result };
  }
  const nickname = result.nickname || account.name || '';
  const notes = normalizeXiaohongshuNotes(result.notes, userId, nickname);
  if (notes.length === 0) {
    return { ok: false, reason: 'normalize notes empty', result };
  }
  clearXiaohongshuLoginDead('notes ok');
  return { ok: true, nickname, notes, result };
}

async function xiaohongshuPageSnapshot(targetPage, userId) {
  return targetPage.evaluate((fallbackUserId) => {
    function unwrap(value, depth = 0) {
      if (depth > 8 || value == null || typeof value !== 'object') return value;
      if ('_value' in value && 'dep' in value) return unwrap(value._value, depth + 1);
      if ('value' in value && 'dep' in value) return unwrap(value.value, depth + 1);
      if (Array.isArray(value)) return value.map((item) => unwrap(item, depth + 1));
      const result = {};
      for (const key of Object.keys(value)) {
        if (key === 'dep' || key.startsWith('__')) continue;
        try { result[key] = unwrap(value[key], depth + 1); } catch {}
      }
      return result;
    }
    const state = { user: unwrap(window.__INITIAL_STATE__?.user || {}) };
    // Strict live detection: profile "正在直播" badge / live room entry only.
    // Old logic treated any live*.xiaohongshu.com link (incl. footer/history) as live → false end/start.
    const headerRoot = document.querySelector(
      '[class*="user-info" i], [class*="userInfo" i], [class*="profile-header" i], [class*="profileHeader" i], [class*="user-red" i], header, [data-e2e*="user" i]'
    ) || document.body;
    const headerText = String(headerRoot?.innerText || headerRoot?.textContent || '');
    const badgeLive = /正在直播|直播中/.test(headerText);
    const liveAnchors = [...document.querySelectorAll('a[href]')].filter((anchor) => {
      const href = String(anchor.href || '');
      if (!/live\.xiaohongshu\.com\//i.test(href) && !/xiaohongshu\.com\/live\//i.test(href)) return false;
      // Skip generic marketing / non-room links
      if (/\/live\/?(?:\?|$)/i.test(href) && !/live\.xiaohongshu\.com\/[a-zA-Z0-9_-]{4,}/i.test(href) && !/\/live\/[a-zA-Z0-9_-]{4,}/i.test(href)) {
        return false;
      }
      const label = `${anchor.textContent || ''} ${anchor.getAttribute('aria-label') || ''} ${anchor.getAttribute('title') || ''}`;
      const inHeader = Boolean(anchor.closest(
        '[class*="user-info" i], [class*="userInfo" i], [class*="profile-header" i], [class*="profileHeader" i], [class*="avatar" i], [data-e2e*="user" i]'
      ));
      return inHeader || /正在直播|直播中|进入直播间|看直播/i.test(label);
    });
    const liveAnchor = liveAnchors[0] || null;
    // State-based fallback (more reliable than random DOM links when available)
    const user = state.user || {};
    const page = user.userPageData || user.userInfo || user || {};
    const basic = page.basicInfo || page.basic_info || page || {};
    const liveFromState = Boolean(
      basic.live?.liveLink || basic.live?.roomId || basic.live?.live_link
      || basic.liveInfo?.roomId || basic.live_info?.room_id
      || page.live?.liveLink || page.liveInfo?.roomId
      || user.live?.roomId
    );
    const liveUrlFromState = String(
      basic.live?.liveLink || basic.live?.live_link || page.live?.liveLink
      || basic.liveInfo?.liveLink || ''
    ).trim();
    const liveActive = liveFromState || badgeLive || Boolean(liveAnchor);
    const liveUrl = liveUrlFromState || liveAnchor?.href || '';
    const profileMatch = location.pathname.match(/\/user\/profile\/([a-zA-Z0-9]+)/);
    return {
      state,
      userId: profileMatch?.[1] || fallbackUserId,
      liveActive,
      liveUrl,
    };
  }, userId);
}

async function scanXiaohongshuAccount(account) {
  const userId = String(account.userId || '').trim();
  if (!userId) return;
  const profileUrl = account.profileUrl || `https://www.xiaohongshu.com/user/profile/${encodeURIComponent(userId)}`;

  // Notes path is API-only (page-context signed user_posted on kept explore tab).
  // User rule 2026-07-19: do NOT use per-account profile goto as notes fallback.
  // - login dead  → emit re-login / account_error
  // - sign cache / transient API fail → refresh+retry once inside ViaAPI; else surface error
  try {
    const api = await scanXiaohongshuAccountViaAPI(account);
    if (api.ok) {
      xiaohongshuLastScanAnyOk = true;
      clearXiaohongshuLoginDead('notes ok');
      log(`xiaohongshu notes via API user=${api.nickname || userId} count=${api.notes.length}`);
      try {
        // Only successful notes may refresh the "good session" disk snapshot.
        await persistStorageState({ force: false, reason: `notes_ok:${userId}` });
        log(`xiaohongshu notes ok → persistStorageState user=${api.nickname || userId} count=${api.notes.length}`);
      } catch (error) {
        log(`xiaohongshu notes persist failed: ${error.message}`);
      }
      emit('xiaohongshu_account', {
        userId,
        profileUrl,
        nickname: api.nickname || account.name || '',
        liveActive: false,
        liveUrl: '',
      });
      emit('xiaohongshu_notes', {
        userId,
        nickname: api.nickname || account.name || '',
        notes: api.notes,
      });
      return;
    }

    const reason = String(api.reason || 'user_posted failed');
    const signMeta = api.result?.signMeta || {};
    const commonLen = Number(signMeta.commonLen || xiaohongshuSignCache.xsCommon?.length || 0);
    const code = String(api.result?.code ?? '');
    const status = Number(api.result?.status || 0);
    const msg = `${reason} ${api.result?.msg || ''} ${code}`;
    const opaque461 = status === 461
      || (status >= 400 && status < 500 && (code === '0' || code === '') && /成功|success/i.test(String(api.result?.msg || reason)));
    // Prefer explicit loginDead from API path; never treat opaque 461/"成功" as re-login.
    const needLogin = !opaque461 && (
      Boolean(api.loginDead)
      || (
        Boolean(api.blocked)
        && /登录已过期|无登录信息|未登录|重新登录|-100|-101/i.test(msg)
        && !/Account abnormal|账号异常|300011|300012|IP存在风险|安全限制|461/i.test(msg)
      )
    );
    const risk = Boolean(api.risk) || opaque461 || /300012|安全限制|IP存在风险|访问频次|风控|持续 461/i.test(msg);
    // 300011 with empty common is sign-cache cold, not "must re-login now".
    const signCold = !risk && !needLogin && (commonLen === 0 || /mnsv2-only|no-sign|commonLen.:0/i.test(JSON.stringify(signMeta)));

    xiaohongshuLastScanAnyFail = true;
    if (needLogin) {
      xiaohongshuLastScanAnyLogin = true;
      markXiaohongshuLoginDead(reason);
    } else if (risk) {
      // Do not flip login-dead for IP/461 — user login cookies may still be fine.
      xiaohongshuLastScanAnyLogin = true;
    }

    // CRITICAL: never persist failed/login-dead state over a previously good session.
    // (Old code forced persist on every scan and could wipe good cookies.)

    let message;
    if (needLogin) {
      message = '小红书需重新登录，笔记列表暂不可用（已停止刷新小红书页面）';
    } else if (risk) {
      message = opaque461
        ? '小红书 API 返回 461/环境异常（不一定是掉登录；请勿反复扫码，先检查代理/IP）'
        : '小红书风控/安全限制：IP存在风险，请切换可靠网络环境后重试（非签名问题，重启无效）';
    } else if (signCold) {
      message = `小红书签名环境未就绪（API 失败，commonLen=${commonLen}），保留 explore 页下次重试，不 goto 主页兜底`;
    } else {
      message = `小红书 API 拉帖失败：${reason}`;
    }

    log(`xiaohongshu API miss for ${account.name || userId}: ${reason} blocked=${Boolean(api.blocked)} needLogin=${needLogin} risk=${risk} signCold=${signCold} loginDead=${xiaohongshuLoginDead} sign=${JSON.stringify(signMeta)}; no-goto-fallback`);
    emit('xiaohongshu_account_error', { userId, message });
    if (needLogin) {
      emit('xiaohongshu_status', { status: 'login_required', message });
    } else if (risk) {
      emit('xiaohongshu_status', {
        status: 'ip_risk',
        message,
      });
    }
    emit('xiaohongshu_account', {
      userId,
      profileUrl,
      nickname: account.name || '',
      liveActive: false,
      liveUrl: '',
    });
    return;
  } catch (error) {
    xiaohongshuLastScanAnyFail = true;
    log(`xiaohongshu API error for ${account.name || userId}: ${error.message}; no-goto-fallback`);
    emit('xiaohongshu_account_error', {
      userId,
      message: `小红书 API 异常：${error.message}`,
    });
    emit('xiaohongshu_account', {
      userId,
      profileUrl,
      nickname: account.name || '',
      liveActive: false,
      liveUrl: '',
    });
    return;
  }
}

/** Consecutive full-scan rounds where every account failed API (not login). Used to ask Go for sidecar restart. */
let xiaohongshuFullFailRounds = 0;
let xiaohongshuLastStuckEmitAt = 0;
let xiaohongshuLastScanAnyOk = false;
let xiaohongshuLastScanAnyFail = false;
let xiaohongshuLastScanAnyLogin = false;

async function scanAllXiaohongshuInner() {
  if (shuttingDown || !settings.xiaohongshuEnabled || settings.xiaohongshuAccounts.length === 0) return;
  if (xiaohongshuScanning) return;
  xiaohongshuScanning = true;
  xiaohongshuLastScanAnyOk = false;
  xiaohongshuLastScanAnyFail = false;
  xiaohongshuLastScanAnyLogin = false;
  try {
    await startBrowser();
    await pruneOrphanProfilePages();
    for (const account of settings.xiaohongshuAccounts) {
      if (shuttingDown) break;
      await scanXiaohongshuAccount(account);
    }
    if (xiaohongshuLastScanAnyOk) {
      xiaohongshuFullFailRounds = 0;
    } else if (xiaohongshuLastScanAnyFail && !xiaohongshuLastScanAnyLogin) {
      xiaohongshuFullFailRounds += 1;
      log(`xiaohongshu full-scan fail streak=${xiaohongshuFullFailRounds}`);
      // Do NOT emit api_stuck / request Chromium restart — that wiped login and
      // made risk worse (user 2026-07-20). Stay on slow poll + pure API.
      if (xiaohongshuFullFailRounds >= 3 && Date.now() - xiaohongshuLastStuckEmitAt > 15 * 60_000) {
        xiaohongshuLastStuckEmitAt = Date.now();
        emit('xiaohongshu_status', {
          status: 'degraded',
          message: `小红书 API 连续 ${xiaohongshuFullFailRounds} 轮拉帖失败（保持浏览器，不侧重启）`,
        });
      }
    }
  } finally {
    xiaohongshuScanning = false;
  }
}

async function scanAllXiaohongshu() {
  // Plan A re-login pause: touch this file to stop auto scans without fighting config.json Save().
  try {
    await fs.access('/root/pocket48-bot/storage/xhs-scan-paused');
    log('xiaohongshu scan paused (storage/xhs-scan-paused present)');
    return;
  } catch {}
  return runProfileScans('xiaohongshu');
}

function scheduleXiaohongshu() {
  clearInterval(xiaohongshuTimer);
  if (!settings.xiaohongshuEnabled) return;
  // Default/floor 60s — user prefers ~1 minute API polls (was 300s during risk lockdown).
  const seconds = Math.max(60, Number(settings.xiaohongshuPollSeconds) || 60);
  xiaohongshuTimer = setInterval(() => { void scanAllXiaohongshu(); }, seconds * 1000);
  log(`xiaohongshu poll scheduled every ${seconds}s (API path, no aggressive restart)`);
}

async function requestXiaohongshuLoginQRCode() {
  // User is actively logging in — allow navigation for QR only.
  clearXiaohongshuLoginDead('user requested login QR');
  const loginPage = await getXiaohongshuPage();
  await loginPage.goto('https://www.xiaohongshu.com/explore', { waitUntil: 'domcontentloaded', timeout: 45_000 });
  await applyXiaohongshuLocalStorage(loginPage);
  const health = await xiaohongshuSessionHealthy();
  if (health.ok && !health.soft) {
    await persistStorageState({ force: true, reason: 'already_logged_in_verified' });
    log('xiaohongshu already logged in (verified) → forced persistStorageState');
    emit('xiaohongshu_status', { status: 'healthy', message: '小红书浏览器已登录' });
    void scanAllXiaohongshu();
    return;
  }
  if (health.ok && health.soft) {
    // Cookies present but me not verified — still allow QR if page looks logged out.
    const url = pageUrlSafe(loginPage);
    if (!/\/login|website-login/i.test(url)) {
      await persistStorageState({ force: true, reason: 'already_logged_in_soft' });
      log('xiaohongshu already logged in (soft cookies) → forced persistStorageState');
      emit('xiaohongshu_status', { status: 'ready', message: '小红书 Cookie 已有，等待 notes 验证' });
      void scanAllXiaohongshu();
      return;
    }
  }
  for (const selector of ['text=登录', 'button:has-text("登录")']) {
    try { await loginPage.locator(selector).first().click({ timeout: 3_000 }); break; } catch {}
  }
  await loginPage.waitForTimeout(1_000);
  const selectors = ['[class*="qrcode" i] img', '[class*="qr-code" i] img', 'img[src^="data:image"]', 'canvas'];
  let image;
  for (const selector of selectors) {
    const candidates = loginPage.locator(selector);
    const count = Math.min(await candidates.count(), 10);
    for (let i = 0; i < count; i += 1) {
      try {
        const candidate = candidates.nth(i); const box = await candidate.boundingBox();
        if (box && box.width >= 100 && box.height >= 100) { image = await candidate.screenshot({ type: 'png' }); break; }
      } catch {}
    }
    if (image) break;
  }
  if (!image) image = await loginPage.screenshot({ type: 'png' });
  emit('xiaohongshu_qrcode', { imageBase64: image.toString('base64'), expiresIn: 300 });
  const deadline = Date.now() + 5 * 60_000;
  while (!shuttingDown && Date.now() < deadline) {
    await loginPage.waitForTimeout(2_000);
    // Do not navigate during wait — user may be entering captcha.
    const cookies = await readXiaohongshuCookieMap();
    if (!(cookies.get('a1') && cookies.get('web_session'))) continue;
    // Prefer verified me when page is already on xhs (no forced goto).
    const url = pageUrlSafe(loginPage);
    let verified = false;
    if (/xiaohongshu\.com/i.test(url) && !/\/login|website-login/i.test(url)) {
      const h = await xiaohongshuSessionHealthy();
      verified = Boolean(h.ok);
    } else {
      // Cookie appeared after scan — treat as provisional success and persist.
      verified = true;
    }
    if (verified) {
      await captureXiaohongshuLocalStorage(loginPage);
      // Require web_session before claiming login success / writing disk.
      const cookies2 = await readXiaohongshuCookieMap();
      if (!(cookies2.get('a1') && cookies2.get('web_session'))) {
        log('xiaohongshu login candidate without web_session yet — keep waiting');
        continue;
      }
      const wrote = await persistStorageState({ force: true, reason: 'login_success' });
      if (!wrote) {
        log('xiaohongshu login success persist refused — keep waiting/retry');
        continue;
      }
      clearXiaohongshuLoginDead('login success');
      log('xiaohongshu login success → persistStorageState written (cookie+localStorage)');
      emit('xiaohongshu_status', { status: 'healthy', message: '小红书浏览器登录成功' });
      void scanAllXiaohongshu();
      return;
    }
  }
  emit('xiaohongshu_status', { status: 'qrcode_expired', message: '小红书登录二维码已过期' });
}

async function gotoWithRetry(targetPage, url, { timeout = 45_000, attempts = 2 } = {}) {
  let lastError;
  for (let i = 0; i < attempts; i++) {
    try {
      // 'commit' is more reliable than full domcontentloaded on flaky CN mobile sites.
      await targetPage.goto(url, { waitUntil: 'commit', timeout });
      // Best-effort settle; ignore secondary wait timeouts.
      await targetPage.waitForLoadState('domcontentloaded', { timeout: Math.min(15_000, timeout) }).catch(() => {});
      return;
    } catch (error) {
      lastError = error;
      log(`goto retry ${i + 1}/${attempts} failed for ${url}: ${error.message}`);
      await sleep(800 * (i + 1));
    }
  }
  throw lastError || new Error(`goto failed: ${url}`);
}

async function weiboCookiesLookLoggedIn() {
  if (!context) return false;
  try {
    const cookies = await context.cookies(['https://m.weibo.cn/', 'https://weibo.com/', 'https://.weibo.cn/', 'https://.weibo.com/']);
    const map = new Map(cookies.map((c) => [c.name, c.value]));
    // Any of the durable session markers is enough for "still logged in".
    return Boolean(
      map.get('SSOLoginState')
      || (map.get('SUB') && map.get('SUBP'))
      || (map.get('WBPSESS') && map.get('SUB'))
      || map.get('SCF'),
    );
  } catch {
    return false;
  }
}

async function mobileLoginState() {
  // Prefer page check; fall back to cookie markers when m.weibo.cn is slow/blocked.
  try {
    await gotoWithRetry(page, 'https://m.weibo.cn/', { timeout: 45_000, attempts: 2 });
    await page.waitForTimeout(800);
    try {
      const ok = await page.evaluate(async () => {
        const response = await fetch('/api/config', { credentials: 'include' });
        if (!response.ok) return false;
        const body = await response.json();
        return Boolean(body?.data?.login);
      });
      if (ok) return true;
    } catch {}
  } catch (error) {
    log(`mobileLoginState page check failed: ${error.message}`);
  }
  const cookieOk = await weiboCookiesLookLoggedIn();
  if (cookieOk) log('mobileLoginState: page slow/failed, cookies still look logged-in');
  return cookieOk;
}

async function preheatAndPublish(reason) {
  // Soft navigation: one failure must not kill cookie publish if we already have session cookies.
  try {
    await gotoWithRetry(page, 'https://m.weibo.cn/', { timeout: 45_000, attempts: 2 });
    await page.waitForTimeout(600);
  } catch (error) {
    log(`preheat m.weibo.cn skipped: ${error.message}`);
  }
  try {
    await gotoWithRetry(page, 'https://weibo.com/', { timeout: 45_000, attempts: 2 });
    await page.waitForTimeout(600);
  } catch (error) {
    log(`preheat weibo.com skipped: ${error.message}`);
  }

  const mobileCookies = await context.cookies(['https://m.weibo.cn/']);
  const webCookies = await context.cookies(['https://weibo.com/']);
  // If navigations failed hard and cookie jars are empty, surface error to caller.
  if (webCookies.length === 0 && mobileCookies.length === 0 && !(await weiboCookiesLookLoggedIn())) {
    throw new Error('预热失败且无可用微博 Cookie（m.weibo.cn / weibo.com 均不可达）');
  }
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
    await gotoWithRetry(page, 'https://passport.weibo.com/sso/signin?entry=miniblog&source=miniblog', {
      timeout: 45_000,
      attempts: 2,
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
    // Transient page.goto timeouts while cookies still valid: warn in log, keep healthy if possible.
    const cookieOk = await weiboCookiesLookLoggedIn().catch(() => false);
    if (cookieOk) {
      log(`refresh soft-fail (cookies still valid): ${error.message}`);
      try {
        const mobileCookies = await context.cookies(['https://m.weibo.cn/']);
        const webCookies = await context.cookies(['https://weibo.com/']);
        if (webCookies.length + mobileCookies.length > 0) {
          emit('cookies', {
            webCookie: formatCookies(webCookies),
            mobileCookie: formatCookies(mobileCookies),
            reason: `${reason}_soft`,
          });
        }
      } catch {}
      emit('status', { status: 'healthy', message: `微博 Cookie 仍有效（页面刷新超时已忽略）：${error.message}` });
    } else {
      emit('error', { message: `刷新微博登录态失败: ${error.message}` });
    }
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
  clearInterval(xiaohongshuTimer);
  clearInterval(douyinContactSyncTimer);
  clearInterval(browserHousekeepTimer);
  stopDouyinIM();
  try {
    if (douyinContactPersistTimer) await persistDouyinContacts();
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
      // Protocol is {cmd:"..."}; accept {type:"..."} as alias (probe clients often send type).
      const cmd = String(command.cmd || command.type || '').trim();
      if (!cmd) {
        log('ignored empty sidecar command');
        return;
      }
      switch (cmd) {
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
            douyinIMEnabled: command.douyinIMEnabled === true,
            douyinIMPrivateEnabled: command.douyinIMPrivateEnabled === true,
            douyinIMGroupName: command.douyinIMGroupName || '',
            douyinIMGroupNumber: command.douyinIMGroupNumber || '',
            xiaohongshuEnabled: command.xiaohongshuEnabled === true,
            xiaohongshuPollSeconds: command.xiaohongshuPollSeconds || settings.xiaohongshuPollSeconds,
            xiaohongshuAccounts: Array.isArray(command.xiaohongshuAccounts) ? command.xiaohongshuAccounts : [],
            proxyServer: String(command.proxyServer || settings.proxyServer || '').trim(),
          };
          await loadDouyinContacts();
          if (settings.weiboEnabled) {
            scheduleRefresh();
            await refresh({ reason: 'startup' });
          } else {
            clearInterval(refreshTimer);
          }
          scheduleDouyin();
          if (settings.douyinEnabled || settings.douyinIMEnabled) {
            try {
              const loggedIn = await douyinBrowserLoggedIn();
              emit('douyin_status', {
                status: loggedIn ? 'healthy' : 'login_required',
                message: loggedIn ? '抖音浏览器已登录' : '抖音浏览器需要登录',
              });
            } catch (error) {
              emit('douyin_status', { status: 'login_error', message: `抖音登录状态检查失败：${error.message}` });
            }
          }
          if (settings.douyinIMEnabled) {
            await restoreDouyinIMIdentity();
            scheduleDouyinContactSync();
            void startDouyinIM().catch((error) => {
              if (!shuttingDown) emit('douyin_im_status', { status: 'error', message: error.message });
            });
          }
          else stopDouyinIM();
          emit('douyin_status', { status: 'ready', message: '抖音作品监控已就绪' });
          void scanAllDouyin();
          scheduleXiaohongshu();
          if (settings.xiaohongshuEnabled) {
            // Cookie ≠ notes. Only emit login_required when cookie missing;
            // never green "healthy" until notes_ok from a real scan.
            const cookieOk = await xiaohongshuCookiePresent().catch(() => false);
            emit('xiaohongshu_status', {
              status: cookieOk ? 'ready' : 'login_required',
              message: cookieOk
                ? '小红书 Cookie 已有，等待 notes 验证（Cookie≠可拉帖）'
                : '小红书浏览器需要登录',
            });
          } else {
            emit('xiaohongshu_status', { status: 'ready', message: '小红书帖子监控未启用' });
          }
          void scanAllXiaohongshu();
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
        case 'douyin_contacts_sync':
        case 'douyin_contact_sync':
          // Force refresh following list nickname + remark cache.
          void syncDouyinContacts({ force: true });
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
        case 'xiaohongshu_sync':
          // Respect explicit enable flag from Go (was hard-coded true).
          if (Object.prototype.hasOwnProperty.call(command, 'xiaohongshuEnabled')) {
            settings.xiaohongshuEnabled = command.xiaohongshuEnabled !== false;
          } else {
            settings.xiaohongshuEnabled = true;
          }
          settings.xiaohongshuPollSeconds = command.xiaohongshuPollSeconds || settings.xiaohongshuPollSeconds;
          settings.xiaohongshuAccounts = Array.isArray(command.xiaohongshuAccounts) ? command.xiaohongshuAccounts : [];
          scheduleXiaohongshu();
          if (settings.xiaohongshuEnabled) {
            void scanAllXiaohongshu();
          } else {
            log('xiaohongshu_sync: disabled, skip scan/poll');
          }
          break;
        case 'xiaohongshu_diff':
          // Native edith request capture vs our signed user_posted.
          try {
            await startBrowser();
            const userId = String(command.userId || command.user_id || '597804a15e87e70f59fd8c57').trim();
            const p = await getXiaohongshuPage();
            let url = pageUrlSafe(p);
            if ((!url || url === 'about:blank' || !/xiaohongshu\.com/i.test(url))) {
              if (!/\/login|website-login|captcha|verify/i.test(url || '')) {
                await p.goto('https://www.xiaohongshu.com/explore', { waitUntil: 'domcontentloaded', timeout: 45_000 });
                await applyXiaohongshuLocalStorage(p);
                await p.waitForTimeout(1000);
                url = pageUrlSafe(p);
              }
            }
            attachXiaohongshuSignCapture(p);

            // Capture next native edith request headers (sign-related only).
            const nativeHits = [];
            const onReq = (req) => {
              try {
                const u = req.url();
                if (!/edith\.xiaohongshu\.com\/api\//i.test(u)) return;
                const h = req.headers() || {};
                const pick = {};
                for (const k of Object.keys(h)) {
                  const lk = k.toLowerCase();
                  if (/^x-|^cookie$|^referer$|^origin$|^user-agent$|^accept$|^content-type$|sec-ch-ua|sec-fetch/i.test(lk)) {
                    let v = String(h[k] || '');
                    if (lk === 'cookie') {
                      // only names + web_session prefix, never full secrets
                      const names = v.split(';').map((s) => s.trim().split('=')[0]).filter(Boolean);
                      const ws = (v.match(/(?:^|;\s*)web_session=([^;]+)/) || [])[1] || '';
                      pick.cookieNames = names;
                      pick.webSessionPrefix = ws.slice(0, 8);
                      pick.cookieCount = names.length;
                      continue;
                    }
                    if (lk === 'x-s' || lk === 'x-s-common' || lk === 'x-rap-param') {
                      pick[lk] = {
                        len: v.length,
                        prefix: v.slice(0, 12),
                        suffix: v.slice(-8),
                      };
                      continue;
                    }
                    if (v.length > 180) v = `${v.slice(0, 80)}…(len=${v.length})`;
                    pick[lk] = v;
                  }
                }
                nativeHits.push({
                  method: req.method(),
                  url: u.replace(/^https:\/\/edith\.xiaohongshu\.com/, ''),
                  headers: pick,
                  ts: Date.now(),
                });
              } catch {}
            };
            p.on('request', onReq);

            // Nudge SPA to fire native signed APIs (homefeed etc), not login page.
            try {
              await p.evaluate(() => {
                window.scrollBy(0, 600);
                window.scrollBy(0, -200);
              });
              await p.waitForTimeout(1500);
              // Try click explore/tab if present — best effort
              try {
                await p.locator('text=发现').first().click({ timeout: 1500 });
                await p.waitForTimeout(800);
              } catch {}
            } catch {}

            // Wait up to ~6s for native hits
            const waitEnd = Date.now() + 6000;
            while (Date.now() < waitEnd && nativeHits.length < 3) {
              await p.waitForTimeout(300);
            }

            // Our user_posted with full attempt header snapshot
            const pathWithQuery = `/api/sns/web/v1/user_posted?num=10&cursor=&user_id=${encodeURIComponent(userId)}&image_formats=jpg,webp,avif&xsec_token=&xsec_source=`;
            const crypto = await import('node:crypto');
            const md5Path = crypto.createHash('md5').update(pathWithQuery).digest('hex');
            const cache = {
              xsCommon: xiaohongshuSignCache.xsCommon || '',
              rap: xiaohongshuSignCache.rap || '',
              cacheAgeMs: xiaohongshuSignCache.capturedAt ? Date.now() - xiaohongshuSignCache.capturedAt : -1,
              source: xiaohongshuSignCache.source || '',
            };
            const our = await p.evaluate(async ({ apiPath, md5Path, md5Both, customAlphabet, cache }) => {
              const url = `https://edith.xiaohongshu.com${apiPath}`;
              function customB64Encode(str) {
                const bytes = new TextEncoder().encode(str);
                let out = '';
                for (let i = 0; i < bytes.length; i += 3) {
                  const a = bytes[i];
                  const b = i + 1 < bytes.length ? bytes[i + 1] : 0;
                  const c = i + 2 < bytes.length ? bytes[i + 2] : 0;
                  const n = (a << 16) | (b << 8) | c;
                  out += customAlphabet[(n >> 18) & 63];
                  out += customAlphabet[(n >> 12) & 63];
                  out += i + 1 < bytes.length ? customAlphabet[(n >> 6) & 63] : '=';
                  out += i + 2 < bytes.length ? customAlphabet[n & 63] : '=';
                }
                return out;
              }
              function detectSignVersion() {
                try {
                  const m = document.cookie.match(/(?:^|;\s*)webBuild=([^;]+)/);
                  if (m && m[1]) return decodeURIComponent(m[1]).trim();
                } catch {}
                return '4.3.7';
              }
              function packXYS(x3) {
                const raw = x3 == null ? '' : String(x3);
                if (/^XY[SW]_/i.test(raw)) return raw;
                const platform = String(window.xsecplatform || 'Linux');
                const payload = JSON.stringify({
                  x0: detectSignVersion(),
                  x1: 'xhs-pc-web',
                  x2: platform,
                  x3: raw,
                  x4: '',
                });
                return 'XYS_' + customB64Encode(payload);
              }
              function summarizeHeaders(headers) {
                const out = {};
                for (const [k, v] of Object.entries(headers || {})) {
                  const lk = k.toLowerCase();
                  const s = String(v || '');
                  if (lk === 'x-s' || lk === 'x-s-common' || lk === 'x-rap-param') {
                    out[lk] = { len: s.length, prefix: s.slice(0, 12), suffix: s.slice(-8) };
                  } else {
                    out[lk] = s.length > 120 ? `${s.slice(0, 60)}…(len=${s.length})` : s;
                  }
                }
                return out;
              }
              const pathOnly = apiPath;
              const attempts = [];
              // mnsv2
              if (typeof window.mnsv2 === 'function') {
                try {
                  const x3 = window.mnsv2(pathOnly, md5Both, md5Path);
                  const xt = Date.now();
                  const xs = packXYS(x3);
                  const headers = {
                    accept: 'application/json, text/plain, */*',
                    referer: location.href || 'https://www.xiaohongshu.com/',
                    'X-s': xs,
                    'X-t': String(xt),
                  };
                  if (cache.xsCommon) headers['X-S-Common'] = cache.xsCommon;
                  if (cache.rap) headers['x-rap-param'] = cache.rap;
                  const res = await fetch(url, { method: 'GET', credentials: 'include', headers });
                  const body = await res.json().catch(() => ({}));
                  attempts.push({
                    mode: 'mnsv2+cacheCommon',
                    status: res.status,
                    code: body?.code,
                    msg: body?.msg,
                    notes: Array.isArray(body?.data?.notes) ? body.data.notes.length : 0,
                    headers: summarizeHeaders(headers),
                    hasMnsv2: true,
                    xsecplatform: String(window.xsecplatform || ''),
                    x0: '4.3.7',
                  });
                } catch (e) {
                  attempts.push({ mode: 'mnsv2-err', err: String(e?.message || e) });
                }
              } else {
                attempts.push({ mode: 'mnsv2-missing' });
              }
              // Also try native-style: intercept by calling a same-origin path that site uses if any helper exists
              // Probe available sign helpers
              const helpers = {
                mnsv2: typeof window.mnsv2,
                _webmsxyw: typeof window._webmsxyw,
                axios: !!(window.axios && window.axios.get),
                xsecplatform: String(window.xsecplatform || ''),
              };
              // cookie presence on document
              const docCookies = document.cookie.split(';').map((s) => s.trim().split('=')[0]).filter(Boolean);
              return {
                attempts,
                helpers,
                docCookieNames: docCookies,
                href: location.href,
                cache: {
                  commonLen: cache.xsCommon ? cache.xsCommon.length : 0,
                  rapLen: cache.rap ? cache.rap.length : 0,
                  cacheAgeMs: cache.cacheAgeMs,
                  source: cache.source,
                },
              };
            }, {
              apiPath: pathWithQuery,
              md5Path,
              md5Both: md5Path,
              customAlphabet: XHS_CUSTOM_B64,
              cache,
            });

            p.off('request', onReq);

            // me check
            let me = null;
            try {
              me = await p.evaluate(async () => {
                const res = await fetch('https://edith.xiaohongshu.com/api/sns/web/v2/user/me', {
                  credentials: 'include',
                  headers: { accept: 'application/json, text/plain, */*', referer: location.href },
                });
                const body = await res.json().catch(() => ({}));
                return {
                  httpStatus: res.status,
                  code: body?.code,
                  msg: body?.msg,
                  guest: body?.data?.guest,
                  userId: body?.data?.user_id,
                  nickname: body?.data?.nickname,
                };
              });
            } catch (e) {
              me = { err: String(e?.message || e) };
            }

            // Diff summary: header keys present in native vs our
            const nativeSample = nativeHits.slice(0, 5);
            const nativeKeys = new Set();
            for (const hit of nativeSample) {
              Object.keys(hit.headers || {}).forEach((k) => nativeKeys.add(k));
            }
            const ourHeaders = our?.attempts?.[0]?.headers || {};
            const ourKeys = new Set(Object.keys(ourHeaders));
            const onlyNative = [...nativeKeys].filter((k) => !ourKeys.has(k) && k !== 'cookieNames' && k !== 'webSessionPrefix' && k !== 'cookieCount');
            const onlyOurs = [...ourKeys].filter((k) => !nativeKeys.has(k));

            const summary = {
              pageUrl: String(url).slice(0, 160),
              me,
              nativeCount: nativeHits.length,
              nativeSample,
              our,
              onlyNativeHeaderKeys: onlyNative,
              onlyOurHeaderKeys: onlyOurs,
              signCache: {
                commonLen: xiaohongshuSignCache.xsCommon?.length || 0,
                rapLen: xiaohongshuSignCache.rap?.length || 0,
                ageMs: xiaohongshuSignCache.capturedAt ? Date.now() - xiaohongshuSignCache.capturedAt : -1,
                source: xiaohongshuSignCache.source || '',
              },
              userId,
            };
            log(`XHS_DIFF ${JSON.stringify(summary).slice(0, 7000)}`);
            emit('xiaohongshu_diff', summary);
          } catch (error) {
            log(`XHS_DIFF failed: ${error.message}`);
            emit('xiaohongshu_diff', { error: error.message });
          }
          break;
        case 'xiaohongshu_probe':
          // Read-only diagnostics: user/me + one user_posted. Prefer no navigation.
          try {
            await startBrowser();
            const userId = String(command.userId || command.user_id || '597804a15e87e70f59fd8c57').trim();
            const p = await getXiaohongshuPage();
            let url = pageUrlSafe(p);
            const cookieMap = await readXiaohongshuCookieMap();
            const cookieSummary = {
              hasA1: Boolean(cookieMap.get('a1')),
              hasWebSession: Boolean(cookieMap.get('web_session')),
              a1Len: String(cookieMap.get('a1') || '').length,
              webSessionLen: String(cookieMap.get('web_session') || '').length,
              webSessionPrefix: String(cookieMap.get('web_session') || '').slice(0, 8),
            };
            // Only navigate to explore if blank and not login-dead — never to profile.
            if ((!url || url === 'about:blank' || !/xiaohongshu\.com/i.test(url)) && !xiaohongshuLoginDead) {
              if (!/\/login|website-login|captcha|verify/i.test(url)) {
                await p.goto('https://www.xiaohongshu.com/explore', { waitUntil: 'domcontentloaded', timeout: 45_000 });
                await applyXiaohongshuLocalStorage(p);
                await p.waitForTimeout(800);
                url = pageUrlSafe(p);
              }
            }
            const pageInfo = {
              url: String(url).slice(0, 200),
              loginDead: xiaohongshuLoginDead,
              loginDeadReason: xiaohongshuLoginDeadReason,
              signCommonLen: xiaohongshuSignCache.xsCommon?.length || 0,
              signRapLen: xiaohongshuSignCache.rap?.length || 0,
              signAgeMs: xiaohongshuSignCache.capturedAt ? Date.now() - xiaohongshuSignCache.capturedAt : -1,
            };
            // user/me without forcing navigation
            let me = null;
            try {
              me = await p.evaluate(async () => {
                try {
                  const res = await fetch('https://edith.xiaohongshu.com/api/sns/web/v2/user/me', {
                    credentials: 'include',
                    headers: { accept: 'application/json, text/plain, */*', referer: location.href },
                  });
                  const body = await res.json().catch(() => ({}));
                  return {
                    httpStatus: res.status,
                    code: body?.code,
                    msg: body?.msg,
                    guest: body?.data?.guest,
                    userId: body?.data?.user_id,
                    nickname: body?.data?.nickname,
                    redId: body?.data?.red_id,
                  };
                } catch (e) {
                  return { err: String(e?.message || e) };
                }
              });
            } catch (e) {
              me = { err: String(e?.message || e) };
            }
            // One user_posted for single account
            let posted = null;
            try {
              // Temporarily allow API path even if login-dead flag set — probe is explicit.
              const wasDead = xiaohongshuLoginDead;
              if (wasDead) clearXiaohongshuLoginDead('probe allow once');
              posted = await fetchXiaohongshuUserPosted(p, userId, { num: 10 });
              if (wasDead && !(posted?.ok)) {
                markXiaohongshuLoginDead('probe still failed');
              }
            } catch (e) {
              posted = { ok: false, err: String(e?.message || e) };
            }
            const postedSummary = posted ? {
              ok: posted.ok,
              status: posted.status,
              code: posted.code,
              msg: posted.msg,
              notes: Array.isArray(posted.notes) ? posted.notes.length : 0,
              nickname: posted.nickname || '',
              signMeta: posted.signMeta || null,
              attempts: (posted.signMeta?.attempts || posted.attempts || []).slice?.(0, 4) || posted.attempts,
            } : null;
            // Also try a native-ish second call: same page fetch without our wrapper extras if me worked
            log(`XHS_PROBE cookies=${JSON.stringify(cookieSummary)} page=${JSON.stringify(pageInfo)} me=${JSON.stringify(me)} posted=${JSON.stringify(postedSummary)}`);
            emit('xiaohongshu_probe', {
              cookies: cookieSummary,
              page: pageInfo,
              me,
              posted: postedSummary,
              userId,
            });
          } catch (error) {
            log(`XHS_PROBE failed: ${error.message}`);
            emit('xiaohongshu_probe', { error: error.message });
          }
          break;
        case 'xiaohongshu_scan':
          // Only persist when we still have a session; never overwrite good disk
          // state with empty/guest cookies after login death.
          try {
            const wrote = await persistStorageState({ force: false, reason: 'xiaohongshu_scan' });
            log(wrote
              ? 'xiaohongshu_scan → persistStorageState ok'
              : 'xiaohongshu_scan → persistStorageState skipped (protect session)');
          } catch (error) {
            log(`xiaohongshu_scan persist failed: ${error.message}`);
          }
          void scanAllXiaohongshu();
          break;
        case 'xiaohongshu_login':
          try { await requestXiaohongshuLoginQRCode(); }
          catch (error) { emit('xiaohongshu_status', { status: 'login_error', message: error.message }); }
          break;
        case 'persist_state':
        case 'persist':
          try {
            // Manual persist is NOT allowed to wipe XHS web_session.
            const wrote = await persistStorageState({ force: false, reason: 'manual_persist' });
            log(wrote ? 'manual persistStorageState ok' : 'manual persistStorageState skipped (protect session)');
            emit('status', {
              status: wrote ? 'healthy' : 'degraded',
              message: wrote ? '浏览器 storage state 已写盘' : 'storage state 未写入（保护会话，拒绝覆盖无 web_session 快照）',
            });
          } catch (error) {
            emit('error', { message: `persist failed: ${error.message}` });
          }
          break;
        case 'shutdown':
          await shutdown();
          break;
        default:
          // Do not emit error for unknown cmd — probes / old clients would spam QQ/email alerts.
          log(`ignored unknown sidecar command: ${cmd}`);
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
