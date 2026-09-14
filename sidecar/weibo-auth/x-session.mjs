// Session cookies only go to the requesting local panel socket, never broadcasts.
export function xSessionFromCookies(cookies, now = Date.now() / 1000) {
  const eligible = cookies.filter(c => ['x.com', 'twitter.com'].includes(String(c.domain || '').replace(/^\./, '')) && (c.expires == null || c.expires <= 0 || c.expires > now))
  const selected = ['auth_token', 'ct0'].map(name => eligible.find(c => c.name === name && c.domain.replace(/^\./, '') === 'x.com') || eligible.find(c => c.name === name))
  if (selected.some(c => !c?.value || /[\r\n;]/.test(c.value))) return null
  return { cookies: selected.map(c => ({ name: c.name, value: c.value, domain: c.domain, expires: c.expires ?? -1 })) }
}

export async function handleXPanel(context, action) {
  if (!['open', 'sync'].includes(action)) return { error: '请选择打开登录或同步登录态' }
  const session = xSessionFromCookies(await context.cookies(['https://x.com/', 'https://twitter.com/']))
  if (action === 'sync') return session ? { session } : { error: '请先在下方浏览器完成 X 登录及邮箱验证码验证，再同步登录态' }
  let page = context.pages().find(p => { try { return ['x.com', 'twitter.com'].includes(new URL(p.url()).hostname) } catch { return false } })
  if (!page) page = await context.newPage()
  await page.bringToFront()
  await page.goto(session ? 'https://x.com/nekomo_st' : 'https://x.com/i/jf/onboarding/web', { waitUntil: 'domcontentloaded', timeout: 30000 })
  if (!session) await page.locator('input[autocomplete*="username"]:visible, input[type="password"]:visible, input[autocomplete="one-time-code"]:visible').first().waitFor({ state: 'visible', timeout: 8000 })
  await page.bringToFront()
  return { opened: true }
}
