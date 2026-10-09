#!/usr/bin/env node
/**
 * 截取管理面板上的某个元素 → PNG。
 *
 * Usage: node panel_shot.mjs <url> <cookieValue> <selector> <out.png> [width] [extraWaitMs]
 *
 * ★ 为什么需要这个脚本（2026-10-08 用户要求）：
 *   报表自己拼 SVG 画出来的图「很扯淡」—— 与面板上精心排过的
 *   组件完全不是一个质量。与其在 Go 里重画一遍，不如直接把面板
 *   上那一块截下来：所见即所得，改面板就等于改报表。
 *
 * 面板是 React SPA（路由是组件 state，不是 URL），所以走
 *   hash 直达：#/signSnapshot?hours=24&date=2026-10-07
 * 再由 selector 定位到具体卡片，只截那一个元素（不留大片空白）。
 *
 * 会话 Cookie 由调用方（Go）先调 /api/auth/login 拿到再传进来，
 * 这里不碰密码。
 */
import pkg from '../sidecar/weibo-auth/node_modules/playwright/index.js';
const { chromium } = pkg;
import fs from 'fs';
import path from 'path';

const url = process.argv[2];
const cookieValue = process.argv[3];
const selector = process.argv[4];
const outPath = process.argv[5];
const width = parseInt(process.argv[6] || '1000', 10);
const extraWait = parseInt(process.argv[7] || '1200', 10);

if (!url || !cookieValue || !selector || !outPath) {
  console.error('usage: panel_shot.mjs <url> <cookie> <selector> <out.png> [width] [extraWaitMs]');
  process.exit(2);
}

const chromeCandidates = [
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH,
  process.env.CHROME_PATH,
  '/root/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome',
  '/root/.cache/ms-playwright/chromium-1208/chrome-linux64/chrome',
].filter(Boolean);

let executablePath;
for (const p of chromeCandidates) {
  try {
    if (fs.existsSync(p)) {
      executablePath = p;
      break;
    }
  } catch {}
}

const browser = await chromium.launch({
  executablePath,
  headless: true,
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none'],
});

try {
  const context = await browser.newContext({
    viewport: { width: Math.max(900, width), height: 1400 },
    deviceScaleFactor: 2,
  });
  // 会话 cookie：面板是 HttpOnly + SameSite=Strict，必须在 context 上预置
  await context.addCookies([
    {
      name: 'p48_admin',
      value: cookieValue,
      domain: '127.0.0.1',
      path: '/',
      httpOnly: true,
      sameSite: 'Strict',
      secure: false,
    },
  ]);

  const page = await context.newPage();
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: 45000 });

  // SPA：先等登录校验通过，再等目标元素真的出现
  await page.waitForSelector(selector, { timeout: 45000, state: 'visible' });
  // 给图表/字体一点时间稳定（SVG 与中文字体渲染有延迟）
  await page.waitForTimeout(extraWait);

  const el = await page.$(selector);
  if (!el) {
    console.error(`selector not found: ${selector}`);
    process.exit(3);
  }
  const buf = await el.screenshot({ type: 'png' });
  fs.mkdirSync(path.dirname(path.resolve(outPath)), { recursive: true });
  fs.writeFileSync(outPath, buf);
  const st = fs.statSync(outPath);
  process.stdout.write(`ok bytes=${st.size} path=${outPath}\n`);
} finally {
  await browser.close();
}
