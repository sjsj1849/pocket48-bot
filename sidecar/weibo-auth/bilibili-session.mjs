// B 站登录态只需要 SESSDATA 这一个 cookie：它决定请求是否被当作登录用户，
// 而投稿列表接口（x/space/wbi/arc/search）在匿名态会被风控拦掉（-352 / 412），
// 有登录态才拿得到合集之外的新投稿。
const BILI_DOMAINS = new Set(['bilibili.com', 'bilibili.cn']);
const LOGIN_URL = 'https://passport.bilibili.com/login';
const SPACE_URL = 'https://space.bilibili.com/';

const domainOf = cookie => String(cookie?.domain || '').replace(/^\./, '').toLowerCase();

export function bilibiliSessionFromCookies(cookies, now = Date.now() / 1000) {
  const eligible = cookies.filter(cookie =>
    BILI_DOMAINS.has(domainOf(cookie)) && (cookie.expires == null || cookie.expires <= 0 || cookie.expires > now));
  // SESSDATA 的 domain 通常是 .bilibili.com，优先取带点的那个（覆盖所有子域）。
  const sessdata = eligible.find(cookie => cookie.name === 'SESSDATA' && cookie.domain.startsWith('.'))
    || eligible.find(cookie => cookie.name === 'SESSDATA');
  if (!sessdata?.value || /[\r\n;]/.test(sessdata.value)) return null;
  // bili_jct 与 DedeUserID 一并带上：部分接口（如关注状态）会校验 csrf token。
  const optional = ['bili_jct', 'DedeUserID', 'DedeUserID__ckMd5']
    .map(name => eligible.find(cookie => cookie.name === name))
    .filter(cookie => cookie?.value && !/[\r\n;]/.test(cookie.value));
  return {
    cookies: [
      { name: sessdata.name, value: sessdata.value, domain: sessdata.domain, expires: sessdata.expires ?? -1 },
      ...optional.map(cookie => ({
        name: cookie.name, value: cookie.value, domain: cookie.domain, expires: cookie.expires ?? -1,
      })),
    ],
  };
}

// 判断浏览器里是否已经登录：SESSDATA 存在即视为已登录。
export async function bilibiliLoggedIn(context) {
  const session = bilibiliSessionFromCookies(await context.cookies(['https://www.bilibili.com/', 'https://bilibili.com/']));
  return Boolean(session);
}

export async function handleBilibiliPanel(context, action) {
  if (!['open', 'sync'].includes(action)) return { error: '请选择打开登录或同步登录态' };

  const session = bilibiliSessionFromCookies(await context.cookies(['https://www.bilibili.com/', 'https://bilibili.com/']));
  if (action === 'sync') {
    return session
      ? { session }
      : { error: '还没有拿到 B 站登录态。请先在下方浏览器扫码或用短信登录，成功后再点同步登录态' };
  }

  let page = context.pages().find(p => {
    try { return BILI_DOMAINS.has(new URL(p.url()).hostname); } catch { return false; }
  });
  if (!page) page = await context.newPage();
  await page.bringToFront();
  // 已登录直接进空间首页（能立刻看到投稿列表），未登录进登录页。
  await page.goto(session ? SPACE_URL : LOGIN_URL, { waitUntil: 'domcontentloaded', timeout: 30000 });
  if (!session) {
    // 登录页有多个入口，等任意一个可点的登录方式出现即可，不强制具体选择。
    await page
      .locator('.login-scan-box, .login-panel, .login-form, [class*="qrcode"], [class*="qr-code"]')
      .first()
      .waitFor({ state: 'visible', timeout: 10000 })
      .catch(() => {});
  }
  await page.bringToFront();
  return { opened: true };
}
