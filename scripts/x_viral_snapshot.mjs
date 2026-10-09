import fs from 'node:fs/promises'
import path from 'node:path'
import { chromium } from '../sidecar/weibo-auth/node_modules/playwright/index.js'

const [inputPath, outputDir] = process.argv.slice(2)
if (!inputPath || !outputDir) process.exit(2)

const posts = JSON.parse(await fs.readFile(inputPath, 'utf8'))
const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined
const browser = await chromium.launch({ headless: true, executablePath, args: ['--no-sandbox', '--disable-dev-shm-usage'] })
const page = await browser.newPage({ viewport: { width: 760, height: 900 }, deviceScaleFactor: 1.5 })

const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char])
const compact = value => new Intl.NumberFormat('zh-CN').format(Number(value || 0))
const date = value => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(Number(value)))

for (const post of posts) {
  if (!/^\d+$/.test(String(post.id))) continue
  const badges = [post.qualifiesLikes ? '万赞' : '', post.qualifiesReposts ? '万转' : ''].filter(Boolean)
  const media = post.cover ? `<img class="media" src="${escapeHTML(post.cover)}" alt="" />` : ''
  const html = `<!doctype html><html><head><meta charset="utf-8"><style>
    *{box-sizing:border-box}html,body{margin:0;background:#eef1f4;font-family:Arial,"Noto Sans SC","Microsoft YaHei",sans-serif;color:#111827}
    body{padding:28px}.card{width:704px;background:#fff;border:1px solid #d8dee5;border-radius:8px;overflow:hidden;box-shadow:0 7px 24px #18223018}
    .head{display:flex;align-items:center;gap:12px;padding:20px 22px 14px}.avatar{width:45px;height:45px;border-radius:50%;display:grid;place-items:center;background:#111827;color:#fff;font-size:18px;font-weight:800}
    .who{min-width:0;display:grid;gap:3px}.who strong{font-size:17px}.who span{font-size:13px;color:#657180}.xmark{margin-left:auto;font-size:23px;font-weight:800}
    .content{padding:0 22px 17px;font-size:18px;line-height:1.55;white-space:pre-wrap;overflow-wrap:anywhere}.media{display:block;width:100%;max-height:640px;object-fit:contain;background:#f2f4f6;border-top:1px solid #e5e9ed;border-bottom:1px solid #e5e9ed}
    .meta{padding:14px 22px 11px;color:#687482;font-size:13px;border-bottom:1px solid #e5e9ed}.stats{display:flex;align-items:center;gap:24px;padding:15px 22px 18px;font-size:14px}
    .stat strong{font-size:17px;margin-right:5px}.badges{margin-left:auto;display:flex;gap:7px}.badge{padding:5px 8px;border-radius:4px;background:#fff0eb;color:#a6381e;font-weight:750;font-size:12px}
    .foot{padding:10px 22px;background:#f7f9fb;color:#657180;font-size:11px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  </style></head><body><article class="card">
    <header class="head"><div class="avatar">${escapeHTML((post.authorName || post.authorUsername || 'X').slice(0,1).toUpperCase())}</div><div class="who"><strong>${escapeHTML(post.authorName || post.authorUsername)}</strong><span>@${escapeHTML(post.authorUsername)}</span></div><div class="xmark">X</div></header>
    <div class="content">${escapeHTML(post.body || '（无文字正文）')}</div>${media}
    <div class="meta">${escapeHTML(date(post.postedAt))}</div>
    <div class="stats"><span class="stat"><strong>${compact(post.repostCount)}</strong>转发</span><span class="stat"><strong>${compact(post.likeCount)}</strong>赞</span><span class="stat"><strong>${compact(post.viewCount)}</strong>浏览</span><div class="badges">${badges.map(item => `<span class="badge">${item}</span>`).join('')}</div></div>
    <div class="foot">${escapeHTML(post.url)}</div>
  </article></body></html>`
  await page.setContent(html, { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(post.cover ? 1800 : 100)
  const card = page.locator('.card')
  await card.screenshot({ path: path.join(outputDir, `${post.id}.png`), type: 'png' })
}

await browser.close()
