import { chromium } from 'playwright'

const BASE = 'http://127.0.0.1:8787'
const EXEC = '/root/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome'

const password = process.argv[2]

const browser = await chromium.launch({
  executablePath: EXEC,
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
})
const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } })
page.on('console', m => console.log('[console]', m.type(), m.text()))
page.on('pageerror', e => console.log('[pageerror]', e.message))

await page.goto(BASE, { waitUntil: 'domcontentloaded' })
await page.fill('input[type=password]', password)
await page.click('button[type=submit]')
await page.waitForTimeout(2500)

// 侧栏点「浏览器」
const clicked = await page.evaluate(() => {
  const items = Array.from(document.querySelectorAll('button, a'))
  const hit = items.find(el => (el.textContent || '').trim() === '浏览器')
  if (hit) { hit.click(); return true }
  return false
})
console.log('clicked browser nav:', clicked)
await page.waitForTimeout(2000)

// 点 TikTok 登录
const t = await page.evaluate(() => {
  const btns = Array.from(document.querySelectorAll('button'))
  const hit = btns.find(b => (b.textContent || '').includes('TikTok 登录'))
  if (hit) { hit.click(); return true }
  return false
})
console.log('clicked tiktok login:', t)

// 等 canvas 出现
await page.waitForSelector('.browser-canvas canvas', { timeout: 45000 }).catch(() => console.log('!! canvas not found'))
await page.waitForTimeout(9000)

const metrics = await page.evaluate(() => {
  const box = document.querySelector('.browser-canvas')
  const cv = document.querySelector('.browser-canvas canvas')
  const r = box ? box.getBoundingClientRect() : null
  const cr = cv ? cv.getBoundingClientRect() : null
  const cs = cv ? getComputedStyle(cv) : null
  const bs = box ? getComputedStyle(box) : null
  return {
    box: r && { w: Math.round(r.width), h: Math.round(r.height) },
    boxStyle: bs && { display: bs.display, overflow: bs.overflow, height: bs.height, minHeight: bs.minHeight },
    canvas: cr && { w: Math.round(cr.width), h: Math.round(cr.height) },
    canvasAttr: cv && { w: cv.width, h: cv.height },
    canvasStyle: cs && {
      width: cs.width, height: cs.height, maxWidth: cs.maxWidth,
      position: cs.position, display: cs.display,
    },
    canvasCount: document.querySelectorAll('.browser-canvas canvas').length,
    docScroll: { w: document.documentElement.scrollWidth, h: document.documentElement.scrollHeight },
  }
})
console.log('METRICS', JSON.stringify(metrics, null, 2))

await page.screenshot({ path: '/root/pocket48-bot/tmp/novnc_probe.png' })
console.log('screenshot saved')
await browser.close()
