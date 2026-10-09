#!/usr/bin/env node
/**
 * 生成 README 用的管理面板整页截图。
 *
 * 与 panel_shot.mjs 的区别：
 *   panel_shot.mjs 截「单个元素」（给报表卡片用，要精准、无空白）
 *   本脚本截「整个页面」（给 README 用，要能看清导航与内容全貌）
 *
 * 面板是 React SPA，路由是组件 state 而非 URL，所以整页截图必须
 *   先点导航按钮，再等对应页面渲染完成 —— 不能靠 goto 不同 URL。
 *
 * 用法：
 *   node readme_shots.mjs <cookie> <outDir>
 */
import pkg from '../sidecar/weibo-auth/node_modules/playwright/index.js';
const { chromium } = pkg;
import fs from 'fs';
import path from 'path';

const cookieValue = process.argv[2];
const outDir = process.argv[3] || 'docs/screenshots';
const BASE = process.env.PANEL_BASE || 'http://127.0.0.1:8787';

if (!cookieValue) {
  console.error('usage: readme_shots.mjs <cookie> [outDir]');
  process.exit(2);
}

const chromeCandidates = [
  process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH,
  '/root/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome',
  '/root/.cache/ms-playwright/chromium-1208/chrome-linux64/chrome',
].filter(Boolean);

let executablePath;
for (const p of chromeCandidates) {
  if (fs.existsSync(p)) { executablePath = p; break; }
}

const browser = await chromium.launch({
  executablePath,
  headless: true,
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none'],
});

try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 960 },
    deviceScaleFactor: 2,
  });
  await context.addCookies([{
    name: 'p48_admin', value: cookieValue,
    domain: '127.0.0.1', path: '/',
    httpOnly: true, sameSite: 'Strict', secure: false,
  }]);

  const page = await context.newPage();
  await page.goto(BASE, { waitUntil: 'domcontentloaded', timeout: 45000 });

  // 等主区域出现（登录态通过）
  await page.waitForSelector('nav', { timeout: 45000, state: 'visible' });
  await page.waitForTimeout(2500);

  // 整页截图：SWA 布局高度会随内容变，用 fullPage 拿到完整内容
  async function shot(name, note) {
    await page.waitForTimeout(1800);
    const file = path.join(outDir, name);
    const buf = await page.screenshot({ type: 'png', fullPage: true });
    fs.mkdirSync(path.dirname(path.resolve(file)), { recursive: true });
    fs.writeFileSync(file, buf);
    console.log(`ok ${name} ${fs.statSync(file).size}B ${note || ''}`);
  }

  // 点导航按钮切页（SPA 路由靠 state）
  async function navTo(label) {
    const clicked = await page.evaluate((text) => {
      const btns = Array.from(document.querySelectorAll('nav button'));
      const target = btns.find(b => (b.textContent || '').trim().includes(text));
      if (!target) return false;
      target.click();
      return true;
    }, label);
    if (!clicked) { console.log(`  ! nav "${label}" 未找到`); return false; }
    await page.waitForTimeout(2200);
    return true;
  }

  await shot('01-overview.png', '总览');

  if (await navTo('配置')) {
    await shot('02-config.png', '配置页');

    // 配置页的分组导航是 nav.config-tabs 里的 button，
    // 显示文本走 groupLabels 映射（Configuration.tsx:128）：
    //   微博→Weibo  抖音→Douyin  消息出口→Gateway  小红书→Redbook  口袋48→Pocket 48
    // Weverse/X/Instagram/Melon/Bilibili/TikTok 无映射，直接显示原名。
    // 实际配置项数见 platformSettingCounts（PlatformField.tsx:30）：
    //   Weverse 17 / X 3 / Instagram 7 / Melon 8 / Bilibili 5
    const shots = [
      ['Weibo',   '03-config-weibo.png',    '微博配置（超话 / 签到监控 / 动态子标签）'],
      ['Douyin',  '04-config-douyin.png',   '抖音配置（作品 / 直播 / 私信）'],
      ['Gateway', '05-config-outlets.png',  '消息出口：QQ / 飞书'],
      ['Redbook', '06-config-redbook.png',  '小红书配置'],
      ['Weverse', '07-config-weverse.png',  'Weverse 配置'],
      ['Instagram','08-config-instagram.png','Instagram 配置'],
      ['Melon',   '09-config-melon.png',    'Melon 配置'],
      ['Bilibili','10-config-bilibili.png','Bilibili 配置'],
    ];
    for (const [tab, file, note] of shots) {
      const ok = await page.evaluate((text) => {
        const nav = document.querySelector('nav.config-tabs');
        if (!nav) return false;
        const t = Array.from(nav.querySelectorAll('button'))
          .find(b => (b.textContent || '').trim().replace(/\s+/g, '').startsWith(text));
        if (!t) return false;
        t.click();
        return true;
      }, tab);
      if (ok) await shot(file, note);
      else console.log(`  ! 分组 "${tab}" 未找到`);
    }
  }

  if (await navTo('浏览器')) {
    await shot('06-browser.png', '内置浏览器会话');
  }

  if (await navTo('日志')) {
    await shot('07-logs.png', '日志');
  }

  if (await navTo('说明')) {
    await shot('08-docs.png', '面板内说明页');
  }

  console.log('done');
} finally {
  await browser.close();
}
