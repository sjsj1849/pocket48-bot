// Only return the Weverse cookies to the requesting local panel connection.
// Never broadcast them as generic browser events or write them to logs.
export function weverseSessionFromCookies(cookies) {
  const valid = cookies.filter(c => /(^|\.)weverse\.io$/.test(c.domain.replace(/^\./, '')))
  const get = name => valid.find(c => c.name === name)?.value || ''
  return { accessToken: get('we2_access_token'), refreshToken: get('we2_refresh_token'), deviceId: get('we2_device_id') }
}
export async function handleWeversePanel(context, action) {
  if (action === 'open') {
    let page = context.pages().find(p => { try { return /(^|\.)weverse\.io$/.test(new URL(p.url()).hostname) } catch { return false } })
    if (!page) page = await context.newPage()
    await page.goto('https://weverse.io/hearts2hearts/artist', { waitUntil: 'domcontentloaded', timeout: 30000 })
    await page.bringToFront()
    return { opened: true }
  }
  const session = weverseSessionFromCookies(await context.cookies('https://weverse.io/'))
  if (!session.accessToken && !session.refreshToken) return { error: '请先在浏览器登录 Weverse 并加入 Hearts2Hearts 社区' }
  return { session }
}
