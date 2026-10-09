// 抖音 / TikTok 的面板登录入口。
//
// ★ 为什么这两个平台的逻辑比 B 站简单得多（2026-10-04）：
//
//	B 站之所以需要 session-store + 单独一份 cookie，是因为投稿列表接口在
//	匿名态会被风控拦掉（-352 / 412），必须把 SESSDATA 塞进 HTTP 请求头。
//
//	抖音不一样：采集侧走 BrowserBridge，复用同一个浏览器上下文的 storage
//	state（见 internal/logic/douyin_monitor.go 的 SetBrowserBridge），
//	用户在这个浏览器里登录之后，采集请求自然就带上登录态了。
//
//	TikTok 更彻底：采集是 sidecar/tiktok-monitor/collector.py 自己开的匿名
//	上下文（TikTok 本身也不需要登录就能看公开内容），登录态对采集毫无影响。
//
//	所以这里只做两件事：把登录页打开给用户，以及回答「到底登录成功没有」。

// 抖音的 cookie 域。登录过程中 douyin.com 与 iesdouyin.com 两边都会种 cookie，
// 只认一个会漏判（实测 sessionid 有时落在 iesdouyin.com 上）。
const DOUYIN_DOMAINS = new Set(['douyin.com', 'iesdouyin.com']);
const DOUYIN_LOGIN_URL = 'https://www.douyin.com/';

// TikTok 的 cookie 域。ttwid 任何访客都有（匿名也会种），
// 所以它不能作为登录判据 —— 真正的判据是 sessionid。
const TIKTOK_DOMAINS = new Set(['tiktok.com', 'tiktokcdn.com', 'tiktokv.com']);
const TIKTOK_LOGIN_URL = 'https://www.tiktok.com/';

const domainOf = cookie => String(cookie?.domain || '').replace(/^\./, '').toLowerCase();

/**
 * 域名是否属于该平台（含子域）。
 *
 * 参数刻意用包装对象 { domain }，方便同时接受 Playwright 的
 * cookie 对象和裸字符串。
 */
function domainMatches(cookie, domains) {
  const domain = domainOf(cookie);
  for (const candidate of domains) {
    if (domain === candidate || domain.endsWith('.' + candidate)) return true;
  }
  return false;
}

/**
 * 过滤出属于本平台且未过期、值安全的 cookie。
 *
 * ★ cookies 允许是 undefined —— 侧卡里 context.cookies() 理论上总会给数组，
 *   但 Playwright 在页面崩掉/上下文已关闭时会抛或给非数组。
 *   这里统一兜成空数组，避免整条面板命令因一个 undefined 直接崩掉
 *   （那会让用户看到「浏览器操作失败」而不是「未登录」）。
 */
function eligibleCookies(cookies, domains, now) {
  if (!Array.isArray(cookies)) return [];
  return cookies.filter(cookie =>
    cookie && domainMatches(cookie, domains) && alive(cookie, now) && safe(cookie));
}

/** cookie 是否尚未过期（expires 为 -1 / 0 / null 视为会话 cookie，有效）。 */
function alive(cookie, now) {
  const expires = cookie?.expires;
  return expires == null || expires <= 0 || expires > now;
}

/** cookie 值是否值得保留 —— 挡掉会破坏请求头的换行与分号。 */
function safe(cookie) {
  return typeof cookie?.value === 'string' && cookie.value.length > 0 &&
    cookie.value.length <= 4096 && !/[\r\n;]/.test(cookie.value);
}

// -------------------------------------------------------------- 抖音

/**
 * 从浏览器 cookie 里挑出属于抖音的部分。
 *
 * 判据是 sessionid / sessionid_ss —— 抖音匿名访客也会拿到 ttwid、odin_tt 之类，
 * 但只有真正登录才会有 sessionid。
 */
export function douyinSessionFromCookies(cookies, now = Date.now() / 1000) {
  const eligible = eligibleCookies(cookies, DOUYIN_DOMAINS, now);
  const login = eligible.filter(cookie => cookie.name === 'sessionid' || cookie.name === 'sessionid_ss');
  if (login.length === 0) return null;
  return {
    cookies: eligible.map(cookie => ({
      name: cookie.name, value: cookie.value,
      domain: cookie.domain, expires: cookie.expires ?? -1,
    })),
  };
}

export async function douyinLoggedIn(context) {
  return Boolean(douyinSessionFromCookies(await context.cookies(['https://www.douyin.com/'])));
}

export async function handleDouyinPanel(context, action) {
  if (!['open', 'sync'].includes(action)) return { error: '请选择打开登录或同步登录态' };

  const session = douyinSessionFromCookies(await context.cookies(['https://www.douyin.com/']));
  if (action === 'sync') {
    return session
      ? { session }
      : { error: '还没有拿到抖音登录态。请先在下方浏览器登录抖音（扫码即可），成功后再点同步登录态' };
  }

  let page = context.pages().find(p => {
    try { return domainMatches({ domain: new URL(p.url()).hostname }, DOUYIN_DOMAINS); } catch { return false; }
  });
  if (!page) page = await context.newPage();
  await page.bringToFront();
  await page.goto(DOUYIN_LOGIN_URL, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch(() => {});
  // 抖音首页本身就是登录入口（有右上角登录按钮），不必强行跳登录页：
  // 强跳反而可能撞上验证码。打开后 bringToFront 让 noVNC 里能看到即可。
  await page.bringToFront();
  return { opened: true };
}

// -------------------------------------------------------------- TikTok

/**
 * 从浏览器 cookie 里挑出属于 TikTok 的部分。
 *
 * 只有 sessionid / sessionid_ss 才算登录态 —— ttwid 游客也有，
 * 拿它当判据会让状态灯永远亮绿灯，等于没有这个按钮。
 */
export function tiktokSessionFromCookies(cookies, now = Date.now() / 1000) {
  const eligible = eligibleCookies(cookies, TIKTOK_DOMAINS, now);
  if (!eligible.some(cookie => cookie.name === 'sessionid' || cookie.name === 'sessionid_ss')) {
    return null;
  }
  return {
    cookies: eligible.map(cookie => ({
      name: cookie.name, value: cookie.value,
      domain: cookie.domain, expires: cookie.expires ?? -1,
    })),
  };
}

export async function tiktokLoggedIn(context) {
  return Boolean(tiktokSessionFromCookies(await context.cookies(['https://www.tiktok.com/'])));
}

export async function handleTiktokPanel(context, action) {
  if (!['open', 'sync'].includes(action)) return { error: '请选择打开登录或同步登录态' };

  const session = tiktokSessionFromCookies(await context.cookies(['https://www.tiktok.com/']));
  if (action === 'sync') {
    return session
      ? { session }
      : { error: '还没有拿到 TikTok 登录态。请先在下方浏览器登录 TikTok，成功后再点同步登录态' };
  }

  let page = context.pages().find(p => {
    try { return domainMatches({ domain: new URL(p.url()).hostname }, TIKTOK_DOMAINS); } catch { return false; }
  });
  if (!page) page = await context.newPage();
  await page.bringToFront();
  await page.goto(TIKTOK_LOGIN_URL, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch(() => {});
  // TikTok 的登录入口藏在右上角头像菜单里，页面上没有常驻的登录按钮。
  // 等一个可点的登录/注册元素出现，最多等 10 秒，等不到也不失败 ——
  // 用户手动点开菜单登录同样是有效路径。
  await page
    .locator('[data-e2e="profile-icon"], [data-e2e="login-button"], header a[href*="login"], button:has-text("Log in")')
    .first()
    .waitFor({ state: 'visible', timeout: 10000 })
    .catch(() => {});
  await page.bringToFront();
  return { opened: true };
}
