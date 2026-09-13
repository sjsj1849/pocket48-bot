#!/usr/bin/env node
/**
 * Render HTML file → PNG via Playwright Chromium.
 * Usage: node html_to_png.mjs <input.html> <output.png> [width=720]
 *
 * - Prefer #report-card element screenshot (tight, less blank)
 * - Preserve the natural report height for monthly comparisons
 */
import pkg from '../sidecar/weibo-auth/node_modules/playwright/index.js';
const { chromium } = pkg;
import fs from 'fs';
import path from 'path';

const htmlPath = process.argv[2];
const outPath = process.argv[3];
const width = parseInt(process.argv[4] || '720', 10);
if (!htmlPath || !outPath) {
  console.error('usage: html_to_png.mjs <input.html> <output.png> [width]');
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

const html = fs.readFileSync(htmlPath, 'utf8');
const browser = await chromium.launch({
  executablePath,
  headless: true,
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none'],
});

const tmpCard = outPath + '.card.png';
try {
  const page = await browser.newPage({
    viewport: { width: Math.max(640, width), height: 1100 },
    deviceScaleFactor: 2,
  });
  await page.setContent(html, { waitUntil: 'load', timeout: 30000 });
  await page.waitForTimeout(250);

  const card = await page.$('#report-card');
  let buf;
  if (card) {
    buf = await card.screenshot({ type: 'png' });
  } else {
    buf = await page.screenshot({ type: 'png', fullPage: true });
  }
  fs.mkdirSync(path.dirname(path.resolve(outPath)), { recursive: true });
  fs.writeFileSync(tmpCard, buf);

  fs.renameSync(tmpCard,outPath);
  process.stdout.write(`ok bytes=${fs.statSync(outPath).size}\n`);

} finally {
  await browser.close();
  try { fs.unlinkSync(tmpCard); } catch {}
}
