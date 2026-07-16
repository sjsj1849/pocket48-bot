import { chromium } from '../../sidecar/weibo-auth/node_modules/playwright/index.mjs'

const baseURL = process.env.POCKET48_CONSOLE_URL || 'https://pocket48.jiufeng.cloud/'
const password = process.env.POCKET48_CONSOLE_PASSWORD
const output = process.env.POCKET48_SCREENSHOT_DIR || '/tmp'
if (!password) throw new Error('POCKET48_CONSOLE_PASSWORD is required')

const browser = await chromium.launch({ headless: true })
try {
  for (const test of [
    { name: 'desktop', viewport: { width: 1440, height: 1000 } },
    { name: 'mobile', viewport: { width: 390, height: 844 } },
  ]) {
    const page = await browser.newPage({ viewport: test.viewport })
    const errors = []
    page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()) })
    await page.goto(baseURL, { waitUntil: 'networkidle' })
    await page.getByLabel('访问密码').fill(password)
    await page.getByRole('button', { name: '进入控制台' }).click()
    await page.getByRole('heading', { name: '运行总览' }).waitFor()
    errors.length = 0
    await page.screenshot({ path: `${output}/pocket48-${test.name}.png`, fullPage: true })
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth)
    if (overflow) throw new Error(`${test.name}: horizontal overflow detected`)
    await page.getByRole('button', { name: '配置', exact: true }).last().click()
    await page.getByRole('heading', { name: '配置', exact: true }).waitFor()
    await page.getByRole('button', { name: /QChat/ }).click()
    await page.getByText('实时消息链路与 REST 兜底策略').waitFor()
    await page.getByRole('button', { name: '浏览器', exact: true }).last().click()
    await page.getByRole('heading', { name: '浏览器', exact: true }).waitFor()
    await page.getByRole('button', { name: '打开交互会话' }).click()
    await page.waitForTimeout(2500)
    await page.screenshot({ path: `${output}/pocket48-${test.name}-browser.png`, fullPage: true })
    if (errors.length) throw new Error(`${test.name}: console errors: ${errors.join(' | ')}`)
    await page.close()
  }
} finally {
  await browser.close()
}
