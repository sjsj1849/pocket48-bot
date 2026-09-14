import { xSessionFromCookies } from './x-session.mjs'

const codeSelector = 'input[autocomplete="one-time-code"]:visible, input[name*="code"]:visible, input[inputmode="numeric"]:visible, input[maxlength="6"]:visible'
const nextButton = /^(Continue|Next|Verify|Log in|Sign in|继续|下一步|验证|登录)$/i

async function loggedIn(context) {
  return Boolean(xSessionFromCookies(await context.cookies(['https://x.com/', 'https://twitter.com/'])))
}
async function foreground(page) {
  const dialogs = page.locator('[role="dialog"]:visible, [aria-modal="true"]:visible')
  return await dialogs.count() ? dialogs.last() : page
}
async function loginPage(context) {
  const pages = context.pages().filter(page => { try { return ['x.com', 'twitter.com'].includes(new URL(page.url()).hostname) } catch { return false } })
  return pages.at(-1) || await context.newPage()
}
async function inspect(context, page) {
  if (await loggedIn(context)) return { stage: 'authenticated' }
  const body = await page.locator('body').innerText()
  if (/verification code|confirmation code|enter.{0,30}code|验证码|验证代码/i.test(body)) return { stage: 'awaiting_code' }
  if (await page.locator('input[type="password"]:visible').count()) return { stage: 'awaiting_password' }
  if (/phone number or username|enter your.{0,20}username|输入.{0,10}用户名/i.test(body)) return { stage: 'needs_username' }
  return { stage: 'browser_verification_required' }
}

// Only private local worker commands call this; input is never logged or returned.
export async function handleXLogin(context, action, input) {
  if (await loggedIn(context)) return { stage: 'authenticated' }
  const page = await loginPage(context)
  await page.bringToFront()
  if (action === 'start') {
    if (!input.email || !input.password) throw new Error('missing login input')
    await page.goto('https://x.com/i/jf/onboarding/web', { waitUntil: 'domcontentloaded', timeout: 25000 })
    // Background forms can also be visible: scope to the active login dialog.
    const scope = await foreground(page)
    await scope.locator('input[autocomplete*="username"]:visible').last().fill(input.email, { timeout: 10000 })
    await scope.getByRole('button', { name: nextButton }).last().click({ timeout: 10000 })
    await page.waitForTimeout(3000)
  } else if (action === 'verify') {
    if (!/^[A-Za-z0-9]{6}$/.test(input.code || '')) throw new Error('invalid verification code')
    const state = await inspect(context, page)
    if (state.stage !== 'awaiting_code') return state
    const scope = await foreground(page)
    const fields = scope.locator(codeSelector)
    const count = await fields.count()
    if (count === 6) {
      for (let i = 0; i < 6; i++) await fields.nth(i).fill(input.code[i])
    } else if (count > 0) {
      await fields.last().fill(input.code)
    } else {
      await scope.locator('input[type="text"]:visible, input[type="tel"]:visible').last().fill(input.code)
    }
    await scope.getByRole('button', { name: nextButton }).last().click({ timeout: 10000 })
    await page.waitForTimeout(3000)
  } else {
    throw new Error('invalid login action')
  }
  if (await page.locator('input[type="password"]:visible').count()) {
    const scope = await foreground(page)
    await scope.locator('input[type="password"]:visible').last().fill(input.password)
    await scope.getByRole('button', { name: nextButton }).last().click({ timeout: 10000 })
    await page.waitForTimeout(3000)
  }
  return await inspect(context, page)
}
