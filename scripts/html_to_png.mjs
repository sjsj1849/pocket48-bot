#!/usr/bin/env node
/**
 * Render HTML file → PNG via Playwright Chromium.
 * Usage: node html_to_png.mjs <input.html> <output.png> [width=720]
 *
 * - Prefer #report-card element screenshot (tight, less blank)
 * - Pad/crop canvas to mobile 3:4 (width:height) with minimal margins
 */
import pkg from '../sidecar/weibo-auth/node_modules/playwright/index.js';
const { chromium } = pkg;
import fs from 'fs';
import path from 'path';
import { execFileSync } from 'child_process';

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

  // Pillow: fit card into 3:4 canvas, minimal pad, center
  const py = `
from PIL import Image
import sys
src, dst = sys.argv[1], sys.argv[2]
im = Image.open(src).convert('RGBA')
cw, ch = im.size
pad = max(16, int(min(cw, ch) * 0.02))  # ~2% margin, min 16px (device pixels)
need_w = cw + 2 * pad
need_h = ch + 2 * pad
# 3:4 portrait (width:height = 3:4)
if need_w * 4 > need_h * 3:
    canvas_w = need_w
    canvas_h = (canvas_w * 4 + 2) // 3
else:
    canvas_h = need_h
    canvas_w = (canvas_h * 3 + 2) // 4
# even sizes look cleaner
canvas_w += canvas_w % 2
canvas_h += canvas_h % 2
bg = (245, 247, 251, 255)  # #f5f7fb
canvas = Image.new('RGBA', (canvas_w, canvas_h), bg)
x = (canvas_w - cw) // 2
y = (canvas_h - ch) // 2
canvas.paste(im, (x, y), im)
canvas.convert('RGB').save(dst, 'PNG', optimize=True)
print(f'card={cw}x{ch} canvas={canvas_w}x{canvas_h} ratio={canvas_w/canvas_h:.4f}')
`;
  const out = execFileSync('python3', ['-c', py, tmpCard, outPath], { encoding: 'utf8' });
  try { fs.unlinkSync(tmpCard); } catch {}
  const st = fs.statSync(outPath);
  process.stdout.write(`ok bytes=${st.size} path=${outPath} ${out.trim()}\n`);
} finally {
  await browser.close();
  try { fs.unlinkSync(tmpCard); } catch {}
}
