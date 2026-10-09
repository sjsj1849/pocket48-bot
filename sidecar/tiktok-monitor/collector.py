#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
TikTok（洋抖）作品采集器 —— 只做动态监控，不做 IM。

设计要点（全部来自 2026-10-04 的实测，勿凭直觉改）：

1. **必须用 Android Chrome UA + is_mobile=true**。
   TikTok 对桌面 Chrome UA 和 iPhone Safari UA 的 `api/post/item_list`
   会返回 **0 字节**（不是报错，是空响应），只有移动端 Android UA
   才返回完整数据（实测 96 万字节 / 35 条作品）。

2. **不能依赖第三方库算签名**。
   TikTokApi(davidteather) 7.x 的 `user.info()` 返回空对象 -> KeyError: 'id'，
   带 msToken / 加长等待均无效。本模块改用 **Playwright 渲染 + 拦截 XHR**：
   签名由浏览器自己算，我们只读结果，不复刻算法。

3. **视频必须浏览器内 fetch**。
   TikTok CDN 拒绝一切外部客户端：curl 无论加什么 Referer/UA 都是 403
   （504 字节错误页）。而**页面内 `fetch(url, {credentials:'include'})` 返回 200**。
   根因不是缺登录态，而是客户端指纹校验。

4. **不需要登录**。全新访客（0 cookie）访问作品页，TikTok 会自动下发
   4 个匿名 cookie（msToken/ttwid/tt_csrf_token/tt_chain_token），
   下载照样成功。所以登录态是「可选增强」，不是前置条件。

5. **限流**：api/post/item_list 连续请求 5-6 次后持续返回 0 字节，
   5 分钟未恢复。但**单个作品页 /video/{id} 不受此限流影响**，
   因此下载一律走作品页，不走列表接口里的 playAddr。

协议：stdin 收一行 JSON，stdout 输出一行 JSON。
  请求 {"operation": "lookup"|"timeline"|"download"|"detail", ...}
  响应 {"ok": true, "data": {...}} 或 {"ok": false, "error": {"code": "..."}}
"""
from __future__ import annotations

import asyncio
import base64
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

# ---------------------------------------------------------------- 常量

# Android Chrome UA。**这三个值都不能改**，改了接口就返回 0 字节。
# 实测 SM-S918B 这支机型 UA 能稳定拿到数据。
ANDROID_UA = (
    "Mozilla/5.0 (Linux; Android 13; SM-S918B) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36"
)

# 作品列表接口。TikTok 换了路径的话这里要跟着改，
# 但注意：**路径对了 UA 不对，照样返回 0 字节**。
ITEM_LIST_PATH = "/api/post/item_list/"

# 默认监控对象：心连心（Hearts2Hearts）。
DEFAULT_USER = "hearts2hearts"

# 单次拉多少条作品。35 条约 96 万字节，再多容易触发限流。
DEFAULT_LIMIT = 20

# 超时。TikTok 首屏 + XHR 在 2 核机器上偏慢，给足 60 秒。
NAV_TIMEOUT_MS = 60_000

# 下载体积上限，对齐其它平台的 25MB 预算（飞书单文件上限 30MB）。
MAX_DOWNLOAD_BYTES = 25 * 1024 * 1024

# 单次 evaluate 回传的切片大小。1MB -> base64 约 1.33MB，CDP 通道吃得消。
SLICE_BYTES = 1024 * 1024

# 限流冷却。触发 0 字节响应后退避这么久，避免把封禁时间越拖越长。
RATE_LIMIT_COOLDOWN_S = 300


class TikTokError(Exception):
    """带错误码的采集失败，code 会原样传回 Go 侧。"""

    def __init__(self, code: str, message: str = "") -> None:
        super().__init__(message or code)
        self.code = code
        self.message = message or code


def log(*args: Any) -> None:
    """日志走 stderr —— stdout 是协议通道，绝不能污染。"""
    print("[tiktok-monitor]", *args, file=sys.stderr, flush=True)


# ---------------------------------------------------------------- 存储


class Storage:
    """落盘目录：storage/tiktok/

    - state.json          订阅游标（highWater / seen）
    - browser-profile/    Playwright 持久化上下文（登录态，可选）
    - videos/             下载的视频
    """

    def __init__(self, root: Path) -> None:
        self.root = root
        self.videos = root / "videos"
        self.state_path = root / "state.json"
        self.profile = root / "browser-profile"
        for p in (self.root, self.videos):
            p.mkdir(parents=True, exist_ok=True)

    def load_state(self) -> dict[str, Any]:
        if not self.state_path.exists():
            return {"cursors": {}}
        try:
            return json.loads(self.state_path.read_text("utf-8"))
        except Exception as e:  # 状态文件坏了不该让整个采集挂掉
            log("state.json 解析失败，按空状态重建:", e)
            return {"cursors": {}}

    def save_state(self, state: dict[str, Any]) -> None:
        # 先写临时文件再改名，避免进程被杀时留下半截 JSON。
        tmp = self.state_path.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(state, ensure_ascii=False, indent=2), "utf-8")
        tmp.replace(self.state_path)


# ---------------------------------------------------------------- 浏览器


def find_chromium() -> str | None:
    """定位 Chromium 可执行文件。

    为什么需要它：pip 装的 playwright 版本与 /root/.cache/ms-playwright
    里已下载的 Chromium 版本经常对不上（playwright 找 1208、实际只有 1228），
    launch() 会直接报 "Executable doesn't exist"。

    刻意**不硬编码版本号** —— playwright 一升级又会坏。按目录名里的数字排序取最大。
    返回 None 表示交给 playwright 自己找（本地开发、依赖完整的环境）。
    """
    cache = Path(os.environ.get("PLAYWRIGHT_BROWSERS_PATH") or "/root/.cache/ms-playwright")
    if not cache.is_dir():
        return None

    candidates: list[tuple[int, Path]] = []
    for d in cache.glob("chromium*"):
        # 目录名形如 chromium-1228 / chromium_headless_shell-1228
        m = re.search(r"(\d+)$", d.name)
        if not m:
            continue
        # 完整 chromium 优先于 headless_shell：TikTok 在完整版上行为更接近真实浏览器
        for exe in (
            d / "chrome-linux64" / "chrome",
            d / "chrome-linux" / "chrome",
            d / "chrome-headless-shell-linux64" / "chrome-headless-shell",
        ):
            if exe.exists():
                candidates.append((int(m.group(1)), exe))
                break

    if not candidates:
        return None
    # 同版本号时完整 chromium 排在前面（list 顺序 + max 稳定取首个最大值）
    best = max(candidates, key=lambda x: x[0])[1]
    log(f"使用 Chromium: {best}")
    return str(best)



# 复用的 Playwright 实例。整个进程生命周期内只建一次，
# 因为创建浏览器上下文本身就要 1-2 秒，每次调用重建会拖垮轮询。
_browser = None
_playwright = None


async def get_browser(proxy: str | None = None) -> Any:
    """惰性创建全局浏览器实例（带代理时按需重建）。"""
    global _browser, _playwright

    if _browser is not None:
        return _browser

    from playwright.async_api import async_playwright

    if _playwright is None:
        _playwright = await async_playwright().start()

    log("启动 Chromium（UA=Android Chrome，TikTok 只认移动端）")
    _browser = await _playwright.chromium.launch(
        headless=True,
        executable_path=find_chromium(),
        # 服务器在新加坡，出口 IP 是腾讯云 AS132203，
        # TikTok 直连正常，不需要额外代理。
        args=[
            "--no-sandbox",
            "--disable-dev-shm-usage",
            "--disable-blink-features=AutomationControlled",
        ],
        proxy={"server": proxy} if proxy else None,
    )
    return _browser


async def close_browser() -> None:
    global _browser, _playwright
    if _browser is not None:
        await _browser.close()
        _browser = None
    if _playwright is not None:
        await _playwright.stop()
        _playwright = None


async def new_context(proxy: str | None = None, profile: Path | None = None) -> Any:
    """新建浏览器上下文。

    ★ 刻意**不用**登录态（2026-10-04 同账号 A/B 实测，两轮）。

    # 先确认登录态本身是有效的
    页面显示「**已关注**」而不是「关注」；32 个 cookie vs 匿名 7个；
    sessionid 在、msToken 172 字符（匿名只有 124）。
    ⇒注入没问题，登录态确实生效了。

    # 然后逐路径对比（登录态先跑，匿名紧接着做基线）
    ┌────────────────┬──────────────┬──────────────┐
    │                │登录态        │ 匿名         │
    ├────────────────┼──────────────┼──────────────┤
    │ item_list 列表  │ 限流 0 字节   │ ✅ 3 条       │
    │ 作品页 /video/  │ ✅ 有正文     │ ✅ 有正文     │
    │ 作品页封面│ ❌ **空** │ ✅ CDN 地址   │
    └────────────────┴──────────────┴──────────────┘

    结论：登录态在两条路径上都拿不到更好的数据，作品页甚至**丢掉封面**
    （登录态下 rehydration 的 video.cover 结构变了，现有取值取不到）。

    轮询间隔压不动的真正原因是**接口本身的硬限流**（实测连请求 5-6 次后
    持续返回 0 字节约 5 分钟），与是否登录无关。

    面板登录功能仍然有用（人工浏览、核对账号），
    快照落在 storage/tiktok-browser/session.json，采集侧不读它。

    # ★ 实验方法上的教训（别再犯）
    第一版对照是「anon 先跑、login 后跑」，两次只隔几秒 ——
    login 撞上 anon 自己触发的限流窗口，看起来像「登录态被限流」。
    **A/B 必须让后跑的那组不消耗前组的配额**，否则结论无效。
    """
    browser = await get_browser(proxy)
    ctx = await browser.new_context(
        user_agent=ANDROID_UA,
        viewport={"width": 412, "height": 915},
        is_mobile=True,
        has_touch=True,
        device_scale_factor=2.625,
        locale="zh-CN",
    )
    ctx.set_default_timeout(NAV_TIMEOUT_MS)
    return ctx


# ---------------------------------------------------------------- 解析


def _pick(obj: Any, *path: str, default: Any = None) -> Any:
    """安全的多级取值。TikTok 的 JSON 层级很不稳定，任何一层缺失都不该抛异常。"""
    cur = obj
    for key in path:
        if not isinstance(cur, dict):
            return default
        cur = cur.get(key)
    return cur if cur is not None else default


def parse_video(item: dict[str, Any]) -> dict[str, Any]:
    """把一条 item_list 原始记录压成我们需要的字段。

    只挑稳定字段。stats 之类的数值字段 TikTok 常在两个位置之间摇摆，
    这里不做强依赖。
    """
    vid = _pick(item, "id", default="")
    if not vid:
        # 图文（photo mode）没有 video 子对象，直接跳过。
        raise TikTokError("item_malformed", "作品记录缺少 id")

    desc = item.get("desc") or ""
    create_time = item.get("createTime") or 0

    video = item.get("video") or {}
    stats = item.get("stats") or {}
    author = item.get("author") or {}
    music = item.get("music") or {}

    # duration 单位是秒（float）。跨平台去重要靠它对齐 B站/抖音。
    duration = video.get("duration") or 0

    # 清晰度档位。TikTok 的 bitrateInfo 是 list，元素形如
    # {"gear_name":"normal_720","bitrate":1234567,"PlayAddr":{...}}
    # 注意实测 PlayAddr 是**驼峰**，小写的 playAddr 在部分响应里是空的。
    qualities: list[dict[str, Any]] = []
    for br in video.get("bitrateInfo") or []:
        if not isinstance(br, dict):
            continue
        addr = br.get("PlayAddr") or br.get("playAddr") or {}
        url = addr.get("UrlList") or addr.get("urlList") or []
        if not url:
            continue
        qualities.append(
            {
                "gear": br.get("gear_name") or br.get("gearName") or "",
                "bitrate": int(br.get("bitrate") or 0),
                "width": int(addr.get("Width") or addr.get("width") or 0),
                "height": int(addr.get("Height") or addr.get("height") or 0),
                "codec": addr.get("CodecType") or addr.get("codecType") or "",
                "url": url[0] if isinstance(url, list) else url,
            }
        )

    # TikTok 的封面字段名实测是 originCover / cover，值为 {url:...}；
    # dynamicCover 是动图封面（GIF），不适合当首图。
    # 这里多试几个候选名而不是赌一个 —— 字段名不可信是本项目的通用教训。
    cover = ""
    for key in ("originCover", "cover", "animatedCover"):
        cand = video.get(key)
        if isinstance(cand, dict):
            u = cand.get("url") or cand.get("Url") or ""
            if u:
                cover = u
                break
        elif isinstance(cand, str) and cand.startswith("http"):
            cover = cand
            break

    play_addr = video.get("playAddr") or video.get("downloadAddr") or ""
    if isinstance(play_addr, dict):
        lst = play_addr.get("urlList") or []
        play_addr = lst[0] if lst else ""

    return {
        "id": vid,
        "desc": desc,
        "createTime": int(create_time),
        "duration": float(duration) if duration else 0.0,
        "authorId": author.get("id") or "",
        # ★ 优先 nickname（显示名，Hearts2Hearts），uniqueId 只作回退。
#   uniqueId 是 @handle（hearts2hearts，小写），拿来当作者名会让
#   顶栏与其它平台不一致（用户 2026-10-05 指出 TikTok 的 H 没大写）。
        "authorName": (author.get("nickname") or author.get("uniqueId") or "").strip(),
        "authorNickname": author.get("nickname") or "",
        "stats": {
            "play": int(stats.get("playCount") or 0),
            "like": int(stats.get("diggCount") or 0),
            "comment": int(stats.get("commentCount") or 0),
            "share": int(stats.get("shareCount") or 0),
        },
        "musicTitle": music.get("title") or "",
        "cover": cover,
        # 注意：playUrl / qualities **故意不返回**。
        # 这些是带 signature+expire 的短时效签名 URL（实测约 3 天过期），
        # 留着没用还把响应体撑到 90KB+（4 档地址）。下载一律走作品页重新取，
        # 那里拿到的是当下新鲜的签名。
        "width": int(video.get("width") or 0),
        "height": int(video.get("height") or 0),
    }


# ---------------------------------------------------------------- 采集操作


async def op_timeline(
    ctx: Any, username: str, limit: int
) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    """打开用户主页，拦截 api/post/item_list 拿作品列表。

    为什么必须拦截而不是直接 requests 调那个接口：
    TikTok 的 X-Bogus / msToken 签名是浏览器算的，自己复刻必然随版本失效。
    让真浏览器去请求，我们只旁听结果 —— 这是唯一可持续的做法。
    """
    captured: list[dict[str, Any]] = []

    async def on_response(resp: Any) -> None:
        if ITEM_LIST_PATH not in resp.url:
            return
        try:
            body = await resp.json()
        except Exception:
            return
        # body["itemList"] 才是作品数组；空 dict 说明被限流了。
        items = (body or {}).get("itemList") or []
        for it in items:
            if isinstance(it, dict):
                captured.append(it)

    page = await ctx.new_page()
    page.on("response", on_response)

    url = f"https://www.tiktok.com/@{username}"
    log(f"打开 {url}")
    try:
        await page.goto(url, wait_until="domcontentloaded", timeout=NAV_TIMEOUT_MS)
        # 给 XHR 一点时间。移动端 UA 下首屏通常 2-4 秒。
        for _ in range(20):
            if captured:
                break
            await asyncio.sleep(0.5)
    finally:
        # 先摘监听再关，避免关页时的收尾请求污染 captured。
        page.remove_listener("response", on_response)
        await page.close()

    if not captured:
        # 空响应有两种可能：新号没作品 / 被限流。区分开便于上层提示。
        raise TikTokError(
            "rate_limited",
            f"@{username} 未取到作品列表（TikTok 限流时接口返回 0 字节，"
            f"需等待 {RATE_LIMIT_COOLDOWN_S // 60} 分钟）",
        )

    videos: list[dict[str, Any]] = []
    for it in captured:
        try:
            videos.append(parse_video(it))
        except TikTokError:
            # 图文作品没有 video 子对象，跳过即可，不算错误。
            continue

    videos.sort(key=lambda v: v["createTime"], reverse=True)
    if limit > 0:
        videos = videos[:limit]

    user_info = videos[0] if videos else {}
    return (
        {
            "username": username,
            "userId": user_info.get("authorId", ""),
            "nickname": user_info.get("authorNickname", ""),
        },
        videos,
    )


def probe_media(path: Path) -> tuple[float, int, int]:
    """用 ffprobe 读时长/分辨率，失败返回全 0（不抛异常）。

    为什么要多这一步：命中下载缓存时 sidecar 不再走浏览器，
    拿不到 <video> 元素的 duration。而时长是**跨平台去重的判定维度**
    （团名 + 时长差 <= 3s），返回 0 会让整条比对失效 —— 表现为
    同一个视频在多个平台被重复推送。

    ffprobe 不存在或文件损坏时返回 0，调用方会保留页面拿到的值。
    """
    try:
        proc = subprocess.run(
            [
                "ffprobe", "-v", "error",
                "-select_streams", "v:0",
                "-show_entries", "format=duration:stream=width,height",
                "-of", "json", str(path),
            ],
            capture_output=True,
            timeout=15,
        )
        if proc.returncode != 0:
            return 0.0, 0, 0
        data = json.loads(proc.stdout.decode() or "{}")
        seconds = 0.0
        try:
            seconds = float(data.get("format", {}).get("duration") or 0.0)
        except (TypeError, ValueError):
            seconds = 0.0
        width = height = 0
        streams = data.get("streams") or []
        if streams:
            width = int(streams[0].get("width") or 0)
            height = int(streams[0].get("height") or 0)
        return seconds, width, height
    except Exception as e:  # ffprobe 缺失、超时、坏文件都走这里
        log(f"ffprobe 读取 {path.name} 失败：{e}")
        return 0.0, 0, 0


async def op_detail(ctx: Any, video_id: str, username: str, out_dir: Path, with_video: bool = True) -> dict[str, Any]:
    """取单个作品的元数据（正文/封面/作者），并按需下载视频本体。

    **2026-10-04 链接提取专用**：用户直接把 TikTok 作品链接发进 QQ 时走这里。

    与 op_download 的区别：
      - op_download 硬编码用 DEFAULT_USER 打开作品页（只服务监控场景，
        监控对象固定是心连心），并且只回传视频字节数与时长。
      - op_detail 接受链接里的真实用户名（分享链接可能是任何人），
        并且**额外返回正文、封面、作者昵称** —— 这三样是「发链接提取内容」
        的输出主体，光有视频没有意义。

    取址与下载完全复用作品页 + 页面内 fetch（见 op_download 的注释）：
    CDN 拒绝一切外部客户端，这条没有例外。

    with_video=False 时只取元数据不下载 —— 面板的「只读预览」用它，
    省掉几十秒的下载和磁盘占用。
    """
    page_url = f"https://www.tiktok.com/@{username}/video/{video_id}"
    page = await ctx.new_page()
    log(f"打开作品页 {page_url}")

    try:
        # 作品页的 recommend/item_list 是**另一个用户**的作品列表，
        # 但页面自身的 webapp.video-detail 才是这条作品的数据。
        # 两边都挂上，哪个先到用哪个。
        xhr_urls: list[str] = []
        detail_urls: list[str] = []

        async def _on_resp(r: Any) -> None:
            if "recommend/item_list" in r.url:
                try:
                    body = await r.json()
                except Exception:
                    return
                for it in ((body or {}).get("itemList") or []):
                    if not isinstance(it, dict) or str(it.get("id")) != video_id:
                        continue
                    v = it.get("video") or {}
                    pa = v.get("playAddr") or v.get("downloadAddr") or {}
                    lst = (pa.get("urlList") or []) if isinstance(pa, dict) else []
                    if lst:
                        xhr_urls.append(lst[0])

            if "item/detail" in r.url or "video/detail" in r.url:
                detail_urls.append(r.url)

        page.on("response", _on_resp)

        await page.goto(page_url, wait_until="domcontentloaded", timeout=NAV_TIMEOUT_MS)

        # 等 <video> 出现：页面开始播就说明数据到了。
        # 实测 __UNIVERSAL_DATA_FOR_REHYDRATION__ 可能是空对象，等它没意义。
        try:
            await page.wait_for_function(
                """() => {
                    const el = document.querySelector('video');
                    return !!(el && (el.currentSrc || el.src));
                }""",
                timeout=25_000,
            )
        except Exception:
            log(f"{video_id} 等待 <video> 超时，仍尝试取元数据")

        # ---- 元数据 + 播放地址 ----
        # 一次 evaluate 全取回来，避免多次过 CDP 通道。
        meta = await page.evaluate(
            """() => {
                const out = {desc:'', cover:'', author:'', authorId:'',
                             url:'', seconds:0, width:0, height:0};

                // 第 1 级：rehydration 里的 webapp.video-detail（作品自身数据）。
                const st = window.__UNIVERSAL_DATA_FOR_REHYDRATION__;
                const scope = (st && st.__DEFAULT_SCOPE__) || {};
                const vd = scope['webapp.video-detail'];
                const item = (vd && vd.itemInfo && vd.itemInfo.itemStruct) || {};
                if (item.desc)  out.desc = String(item.desc);
                if (item.id)    out.url = 'https://www.tiktok.com/@' +
                                    ((item.author && item.author.uniqueId) || '') +
                                    '/video/' + item.id;
                if (item.author) {
                    out.author = String(item.author.nickname || '');
                    out.authorId = String(item.author.uniqueId || '');
                }
                const v = item.video || {};
                if (v.cover) {
                    const cl = v.cover.urlList || [];
                    if (cl.length) out.cover = cl[0];
                }
                if (v.originCover) {
                    const ol = v.originCover.urlList || [];
                    if (ol.length && !out.cover) out.cover = ol[0];
                }
                if (v.duration) out.seconds = Number(v.duration);
                if (v.width)  out.width  = Number(v.width);
                if (v.height) out.height = Number(v.height);

                // 第 2 级：DOM 上的 OG 标签。rehydration 为空时它通常还在，
                // 这是实测的又一层退路（OG 至少有 desc 与封面）。
                if (!out.desc || !out.cover || !out.author) {
                    // h1 是原始正文，比 OG 干净得多，优先。
                    // 实测 h1 存在而 data-e2e="video-desc" 不存在，
                    // 所以选择器以 h1 打头。
                    if (!out.desc) {
                        const h1 = document.querySelector('h1');
                        if (h1) {
                            const t = (h1.innerText || '').trim();
                            if (t && t.length < 3000) out.desc = t;
                        }
                    }
                    const ogDesc = document.querySelector(
                        'meta[property="og:description"], meta[name="description"]');
                    if (ogDesc && ogDesc.content && !out.desc) out.desc = ogDesc.content;
                    const ogImg = document.querySelector('meta[property="og:image"]');
                    if (ogImg && ogImg.content && !out.cover) out.cover = ogImg.content;
                    // og:title 形如 "TikTok · Hearts2Hearts"（实测 2026-10-04）。
                    //
                    // ★ 这里原来按 ' - ' 切，是从别的站点抄来的形状，
                    //   对 TikTok 根本不成立 —— 切不出来，out.author 保持空，
                    //   于是流程落到下一级（DOM 昵称），而那一级的选择器又是错的
                    //   （见下），两道门连着漏，最终作者署名退化成链接里的 username。
                    //   实测教训：外部接口的字段形状只能靠抓真实页面确认，
                    //   「看起来应该长这样」的推测一律不成立。
                    const ogTitle = document.querySelector('meta[property="og:title"]');
                    if (ogTitle && ogTitle.content && !out.author) {
                        const parts = ogTitle.content.split('·').map(s => s.trim());
                        if (parts.length >= 2 && parts[0].toLowerCase() === 'tiktok') {
                            out.author = parts.slice(1).join('·').trim();
                        } else if (parts.length >= 2) {
                            out.author = parts[parts.length - 1].trim();
                        }
                    }
                }

                // 第 3 级：页面上的显示昵称（实测这是最可靠的一级）。
                //
                // 真实 HTML（2026-10-04 抓 hearts2hearts 作品页得到）：
                //   <a href="/@hearts2hearts">
                //     <div class="...DivUserName">
                //       <span class="...SpanTagText">@</span><span>Hearts2Hearts</span>
                //     </div>
                //   </a>
                // innerText = "@Hearts2Hearts"
                //
                // ★ 原来查的是 `a[href*="/@"] strong`，而真实 DOM 里
                //   **根本没有 strong 标签** —— 昵称是纯 div+span。
                //   选择器失配又不报错，作者署名就一路退化成链接里的
                //   username（hearts2hearts，小写），署名给用户看很别扭。
                //
                // 现在的判据（每一条都是实测踩出来的）：
                //   - href 必须是精确的 "/@用户名"（作品链接 "/@x/video/1" 不算）
                //   - innerText 以 @ 开头，长度 <= 60
                //   - 排除纯数字（那是点赞数 199K 之类）和按钮文案
                if (!out.author) {
                    const cand = document.querySelectorAll('a[href^="/@"]');
                    for (const el of cand) {
                        const href = el.getAttribute('href') || '';
                        // 精确作者主页链接：/@name，不能是 /@name/video/123
                        if (!/^\/@[A-Za-z0-9_.]+$/.test(href)) continue;
                        const raw = (el.innerText || '').trim();
                        const t = raw.startsWith('@') ? raw.slice(1).trim() : raw;
                        if (!t || t.length > 60) continue;
                        if (/^\d/.test(t)) continue;          // 199K / 3032
                        if (/^(打开|打开应用|关注|分享|评论)/.test(t)) continue;
                        out.author = t;
                        break;
                    }
                }

                // 正文兜底：OG description 常常是「1.2M 获赞，... 来自 X (@y)
                // 的 TikTok 视频：「正文」。曲名。」这种带统计数字的整段，
                // 直接当正文发出去很难看。页面里 meta[property="og:description"]
                // 是同一个值，所以真正的兜底是页面里的作品描述节点。
                // ★ 实测（2026-10-04）：作品页上 `[data-e2e="video-desc"]`
                //   在移动端 UA 下**根本不存在**，真正的正文节点是 h1，
                //   innerText = "낼 봥 ㅋㅋ ♡ #Hearts2Hearts ..."，已经不含
                //   统计数字与宣传语，是三者中最干净的正文来源。
                //   旧代码把 h1 放在候选里但位置排在后面，且真值判断在
                //   OG 之后 —— OG 总能拿到值（见下），所以 h1 永远轮不到，
                //   于是正文用了 OG 那串带「199K 获赞，3032 评论。来自 X 的
                //   TikTok 视频：…”的整段，还要靠 Go 侧正则硬洗，很脆。
                //
                // 现在把 h1 提到 OG 之前：它是**原始正文**，
                // 不需要任何清洗，也不会因为 TikTok 改文案而失配。
                if (!out.desc) {
                    const d = document.querySelector(
                        'h1[data-e2e], h1[data-e2e="video-desc"], [data-e2e="video-desc"]');
                    if (d) {
                        const t = (d.innerText || '').trim();
                        if (t && t.length < 3000) out.desc = t;
                    }
                }

                // 播放地址（沿用 op_download 的三级递进）。
                const el = document.querySelector('video');
                if (el) {
                    out.playUrl = el.currentSrc || el.src || '';
                    if (!out.seconds && isFinite(el.duration)) out.seconds = el.duration;
                    if (el.videoWidth && !out.width) {
                        out.width = el.videoWidth; out.height = el.videoHeight;
                    }
                }
                return out;
            }"""
        )

        info = {
            "id": video_id,
            "desc": meta.get("desc") or "",
            "cover": meta.get("cover") or "",
            "author": meta.get("author") or "",
            "authorId": meta.get("authorId") or username,
            "url": meta.get("url") or page_url,
            "duration": float(meta.get("seconds") or 0.0),
            "width": int(meta.get("width") or 0),
            "height": int(meta.get("height") or 0),
        }

        # 正文与封面都没有才算真的取不到内容（视频地址拿不到还能提示用户点链接）。
        if not info["desc"] and not info["cover"] and not meta.get("playUrl"):
            raise TikTokError(
                "detail_empty",
                f"没能从作品页取到 TikTok 作品 {video_id} 的内容，"
                f"可能已删除或需要登录查看。",
            )

        if not with_video:
            info["downloaded"] = False
            return info

        # ---- 下载视频（沿用 op_download 的分片逻辑）----
        target = out_dir / f"{video_id}.mp4"
        if target.exists() and target.stat().st_size > 0:
            log(f"{video_id} 已存在，跳过下载（{target.stat().st_size} 字节）")
            # 同 op_download：命中缓存也要回时长，否则跨平台去重拿不到时长维度。
            seconds, width, height = probe_media(target)
            info.update({
                "path": str(target),
                "bytes": target.stat().st_size,
                "duration": seconds or info.get("duration") or 0.0,
                "width": width or int(info.get("width") or 0),
                "height": height or int(info.get("height") or 0),
                "cached": True,
                "downloaded": True,
            })
            return info

        head = await page.evaluate(
            """async ({maxBytes, fallbackUrl}) => {
                if (window.__ttCache) {
                    return {ok:true, cached:true, size: window.__ttCache.length};
                }
                let url = '';
                const el = document.querySelector('video');
                if (el) url = el.currentSrc || el.src || '';
                if (!url) url = fallbackUrl || '';
                if (!url) return {ok:false, code:'no_url'};
                try {
                    const resp = await fetch(url, {credentials: 'include'});
                    if (!resp.ok) return {ok:false, code:'http_' + resp.status};
                    const buf = await resp.arrayBuffer();
                    if (buf.byteLength > maxBytes) {
                        return {ok:false, code:'too_large', size: buf.byteLength};
                    }
                    window.__ttCache = new Uint8Array(buf);
                    return {ok:true, cached:false, size: window.__ttCache.length};
                } catch (e) {
                    return {ok:false, code:'fetch_failed', msg: String(e)};
                }
            }""",
            # evaluate 只接受一个参数，两个值必须打包成 dict。
            {"maxBytes": MAX_DOWNLOAD_BYTES, "fallbackUrl": xhr_urls[0] if xhr_urls else ""},
        )

        if not head.get("ok"):
            # 下载失败**不丢弃元数据** —— 正文和封面已经拿到了，
            # 让上层照常发文字+封面+链接，比整条失败友好得多。
            code = head.get("code", "unknown")
            log(f"{video_id} 视频下载失败（{code}），但元数据已取到，继续返回")
            info["downloaded"] = False
            info["videoError"] = code
            return info

        total = int(head.get("size") or 0)
        if total < 1024:
            log(f"{video_id} 下载内容仅 {total} 字节，疑似截断，但元数据仍可用")
            info["downloaded"] = False
            info["videoError"] = "truncated"
            return info

        tmp = target.with_suffix(".mp4.part")
        written = 0
        with open(tmp, "wb") as fh:
            while written < total:
                part = await page.evaluate(
                    """({start, len}) => {
                        const c = window.__ttCache;
                        if (!c) return null;
                        const slice = c.subarray(start, Math.min(start + len, c.length));
                        let bin = '';
                        const CHUNK = 0x8000;
                        for (let i = 0; i < slice.length; i += CHUNK) {
                            bin += String.fromCharCode.apply(
                                null, slice.subarray(i, i + CHUNK));
                        }
                        return {b64: btoa(bin), n: slice.length};
                    }""",
                    {"start": written, "len": SLICE_BYTES},
                )
                if not part:
                    raise TikTokError(
                        "download_truncated",
                        f"读取分片时缓存已失效（已取 {written}/{total} 字节）",
                    )
                fh.write(base64.b64decode(part["b64"]))
                written += int(part["n"])
                log(f"{video_id} 下载进度 {written}/{total}")

        if written != total:
            tmp.unlink(missing_ok=True)
            info["downloaded"] = False
            info["videoError"] = "size_mismatch"
            return info

        tmp.replace(target)
        log(f"{video_id} 下载完成 {written} 字节")

        # ★ 下载成功后补一次 ffprobe 覆盖时长/分辨率。
        #   实测踩坑：这里原本只写 path/bytes，而页面里 <video> 的 duration
        #   在 op_detail 的第一次 evaluate 里可能取到 0（<video> 刚出现时
        #   duration 还是 NaN），于是 Go 侧 Item.Duration=0。
        #   而时长是跨平台去重的判定维度（团名 + 时长差 <= 3s），
        #   为 0 会让整个比对失效 —— 表现为同一条视频被重复推送。
        #   代价只是 100ms 级的 ffprobe，远小于漏推的代价。
        seconds, width, height = probe_media(target)
        if seconds:
            info["duration"] = seconds
        if width:
            info["width"] = width
            info["height"] = height or int(info.get("height") or 0)

        info.update({
            "path": str(target),
            "bytes": written,
            "cached": bool(head.get("cached")),
            "downloaded": True,
        })
        return info
    finally:
        await page.close()


async def op_download(ctx: Any, video_id: str, out_dir: Path) -> dict[str, Any]:
    """下载单个作品的视频。

    **必须走作品页 + 页面内 fetch**，原因见模块 docstring 第 3 条。
    外部客户端（curl / Go http.Get / Python requests）一律 403。

    二进制回传用「先取 URL，再分片读区间」而不是一次性 base64：
    116 秒的片子约 8.5MB，base64 后 11MB，一次性 evaluate 返回
    会卡住 CDP 通道。分片后每次只回 1MB，内存和传输都可控。

    另外这里也顺便拿到时长 —— 与列表接口的 duration 互为校验，
    跨平台去重就靠它和 B站/抖音对齐。
    """
    target = out_dir / f"{video_id}.mp4"

    if target.exists() and target.stat().st_size > 0:
        log(f"{video_id} 已存在，跳过下载（{target.stat().st_size} 字节）")
        # ★ 时长/分辨率**必须给**，不能只回 path + bytes。
        #   实测踩坑：这里原本硬编码 duration=0.0，而时长是跨平台去重的
        #   判定维度之一，为 0 会让「团名 + 时长」这条比对直接失效。
        #   代价只是命中缓存时多跑一次 ffprobe（约 100ms），远小于漏推的代价。
        seconds, width, height = probe_media(target)
        return {
            "path": str(target),
            "bytes": target.stat().st_size,
            "duration": seconds,
            "width": width,
            "height": height,
            "cached": True,
        }

    page_url = f"https://www.tiktok.com/@{DEFAULT_USER}/video/{video_id}"
    page = await ctx.new_page()
    log(f"打开作品页 {page_url}")

    try:
        # 作品页一定会发 recommend/item_list，从里面捞播放地址当退路。
        # 页面内 fetch 的地址优先级见下面 JS 里的三级递进。
        xhr_urls: list[str] = []

        async def _on_resp(r: Any) -> None:
            if "recommend/item_list" not in r.url:
                return
            try:
                body = await r.json()
            except Exception:
                return
            for it in ((body or {}).get("itemList") or []):
                if not isinstance(it, dict) or str(it.get("id")) != video_id:
                    continue
                v = it.get("video") or {}
                pa = v.get("playAddr") or v.get("downloadAddr") or {}
                lst = (pa.get("urlList") or []) if isinstance(pa, dict) else []
                if lst:
                    xhr_urls.append(lst[0])

        page.on("response", _on_resp)

        await page.goto(page_url, wait_until="domcontentloaded", timeout=NAV_TIMEOUT_MS)

        # **不要**再等 __UNIVERSAL_DATA_FOR_REHYDRATION__ !== undefined：
        # 实测它存在但内容为空（TikTok 改异步填充了），等它毫无意义。
        # 真正可靠的是等 <video> 元素出现 —— 页面开始播放就一定有地址。
        try:
            await page.wait_for_function(
                """() => {
                    const el = document.querySelector('video');
                    return !!(el && (el.currentSrc || el.src));
                }""",
                timeout=25_000,
            )
        except Exception:
            log(f"{video_id} 等待 <video> 元素超时，将退回 rehydration / XHR 取址")

        # ---- 第 1 步：取播放地址 + 触发浏览器内 fetch ----
        # credentials:'include' 让浏览器带上 TikTok 下发的匿名 cookie，
        # 这是外部请求拿不到的那部分信任凭据。
        head = await page.evaluate(
            """async ({maxBytes, fallbackUrl}) => {
                // 用 window.__ttCache 存二进制，避免反复过 CDP。
                if (window.__ttCache) {
                    return {ok:true, cached:true, size: window.__ttCache.length};
                }

                // ---- 三级递进取址，实测第 1 级最稳 ----
                let url = '';
                let seconds = 0, width = 0, height = 0;

                // 第 1 级：<video> 元素。页面播起来就一定有，
                // 且时长就是播放器的真实时长（实测 116.266667，与抖音/B站一致）。
                const el = document.querySelector('video');
                if (el) {
                    url = el.currentSrc || el.src || '';
                    if (isFinite(el.duration)) seconds = el.duration;
                    if (el.videoWidth)  { width  = el.videoWidth;  height = el.videoHeight; }
                }

                // 第 2 级：rehydration。注意实测它可能是空对象，
                // 所以每层都判空，别假设 __DEFAULT_SCOPE__ 存在。
                if (!url) {
                    const st = window.__UNIVERSAL_DATA_FOR_REHYDRATION__;
                    const scope = (st && st.__DEFAULT_SCOPE__) || {};
                    const vd = scope['webapp.video-detail'];
                    const detail = (vd && vd.itemInfo && vd.itemInfo.itemStruct) || {};
                    const v = detail.video || {};
                    if (v.playAddr && v.playAddr.urlList && v.playAddr.urlList[0]) {
                        url = v.playAddr.urlList[0];
                    } else if (v.downloadAddr && v.downloadAddr.urlList) {
                        url = v.downloadAddr.urlList[0];
                    }
                    if (v.duration) seconds = Number(v.duration);
                    if (v.width)  { width  = Number(v.width);  height = Number(v.height); }
                }

                // 第 3 级：XHR 拦截拿到的 recommend/item_list 地址。
                if (!url) url = fallbackUrl || '';

                if (!url) return {ok:false, code:'no_url'};

                try {
                    const resp = await fetch(url, {credentials: 'include'});
                    if (!resp.ok) return {ok:false, code:'http_' + resp.status};
                    const buf = await resp.arrayBuffer();
                    if (buf.byteLength > maxBytes) {
                        return {ok:false, code:'too_large', size: buf.byteLength};
                    }
                    window.__ttCache = new Uint8Array(buf);
                    return {
                        ok: true, cached: false,
                        size: window.__ttCache.length,
                        duration: seconds, width: width, height: height,
                    };
                } catch (e) {
                    return {ok:false, code:'fetch_failed', msg: String(e)};
                }
            }""",
            # evaluate 只接受一个参数，两个值必须打包成 dict。
            {
                "maxBytes": MAX_DOWNLOAD_BYTES,
                # 第 3 级退路用。
                "fallbackUrl": xhr_urls[0] if xhr_urls else "",
            },
        )

        if not head.get("ok"):
            code = head.get("code", "unknown")
            log(f"{video_id} 下载失败: {code} {head.get('msg','')}")
            raise TikTokError(
                "download_failed",
                f"TikTok 作品 {video_id} 下载失败（{code}）。"
                f"这是 CDN 拒绝外部客户端的典型表现，与登录态无关。",
            )

        total = int(head.get("size") or 0)
        if total < 1024:
            raise TikTokError("download_truncated", f"下载内容仅 {total} 字节，疑似截断")

        # ---- 第 2 步：分片读回，写本地文件 ----
        # 先写临时文件再改名，避免中断留下看起来完整的半个 mp4。
        tmp = target.with_suffix(".mp4.part")
        written = 0
        with open(tmp, "wb") as fh:
            while written < total:
                part = await page.evaluate(
                    """({start, len}) => {
                        const c = window.__ttCache;
                        if (!c) return null;
                        const slice = c.subarray(start, Math.min(start + len, c.length));
                        let bin = '';
                        const CHUNK = 0x8000;
                        for (let i = 0; i < slice.length; i += CHUNK) {
                            bin += String.fromCharCode.apply(
                                null, slice.subarray(i, i + CHUNK));
                        }
                        return {b64: btoa(bin), n: slice.length};
                    }""",
                    # 同样只能传一个参数。
                    {"start": written, "len": SLICE_BYTES},
                )
                if not part:
                    raise TikTokError(
                        "download_truncated",
                        f"读取分片时缓存已失效（已取 {written}/{total} 字节）",
                    )
                fh.write(base64.b64decode(part["b64"]))
                written += int(part["n"])
                # 日志别打太密，8.5MB / 1MB 一片也就 9 行。
                log(f"{video_id} 下载进度 {written}/{total}")

        if written != total:
            tmp.unlink(missing_ok=True)
            raise TikTokError(
                "download_truncated",
                f"写入字节数不符：期望 {total}，实得 {written}",
            )

        tmp.replace(target)
        log(f"{video_id} 下载完成 {written} 字节")

        # 同样用 ffprobe 兜底：页面里的 <video>.duration 在刚出现时是 NaN，
        # 而 TikTokMonitor 的跨平台去重登记依赖 Seconds，0 会让比对失效。
        seconds, width, height = probe_media(target)
        return {
            "path": str(target),
            "bytes": written,
            "duration": seconds or float(head.get("duration") or 0.0),
            "width": width or int(head.get("width") or 0),
            "height": height or int(head.get("height") or 0),
            "cached": bool(head.get("cached")),
        }
    finally:
        # 页面内缓存了几 MB 的 Uint8Array，关页面一并释放。
        await page.close()


# ---------------------------------------------------------------- 主流程


async def dispatch(req: dict[str, Any], storage: Storage) -> dict[str, Any]:
    op = req.get("operation", "")
    proxy = req.get("proxyURL") or None

    ctx = await new_context(proxy)
    try:
        if op == "timeline":
            username = req.get("username") or DEFAULT_USER
            limit = int(req.get("limit") or DEFAULT_LIMIT)
            user, videos = await op_timeline(ctx, username, limit)
            return {"user": user, "videos": videos}

        if op == "download":
            video_id = str(req.get("videoId") or "")
            if not video_id:
                raise TikTokError("bad_request", "缺少 videoId")
            return await op_download(ctx, video_id, storage.videos)

        if op == "detail":
            # 链接提取：作品 ID 来自用户分享的链接，用户名也来自链接
            # （分享链接可能是任何人，不能像 op_download 那样写死 DEFAULT_USER）。
            video_id = str(req.get("videoId") or "")
            if not video_id:
                raise TikTokError("bad_request", "缺少 videoId")
            username = str(req.get("username") or DEFAULT_USER).lstrip("@")
            with_video = bool(req.get("withVideo", True))
            return await op_detail(ctx, video_id, username, storage.videos, with_video)

        raise TikTokError("bad_request", f"未知 operation: {op}")
    finally:
        await ctx.close()


async def _serve(req: dict, storage: Storage) -> None:
    """在**同一个 event loop** 内完成 采集 + 收尾，并把结果写到 stdout。

    ★ 为什么必须这样（2026-10-04 线上踩坑）：
    早先的写法是

        data = asyncio.run(dispatch(req, storage))     # loop #1
        ...
        asyncio.run(close_browser())                   # loop #2

    Playwright 的异步 API（`async_playwright().start()` 拿到的对象）与**创建它
    的那个 event loop 强绑定**。在新的 loop #2 上调 `_playwright.stop()` / 
    `_browser.close()`，driver 收不到 loop 关闭信号，就会一直挂着。

    实测症状极具迷惑性：**数据 7 秒就拿到了**，JSON 也在 15:49:32 打印到了
    stdout，但进程直到 15:51:55 才真正退出（多耗 143 秒）。调用方等的是
    「进程退出」，于是 120s 超时，而日志里最后一行永远停在「打开主页」——
    看起来像 TikTok 慢，其实是收尾挂死。

    所以：采集和收尾必须共用一个 loop，且收尾要有独立超时兜底。
    """
    try:
        data = await dispatch(req, storage)
        print(json.dumps({"ok": True, "data": data}, ensure_ascii=False))
    except TikTokError as e:
        log("采集失败:", e.code, e.message)
        print(json.dumps({"ok": False, "error": {"code": e.code, "message": e.message}},
                         ensure_ascii=False))
    except Exception as e:
        log("未预期异常:", type(e).__name__, e)
        print(json.dumps(
            {"ok": False, "error": {"code": "internal", "message": f"{type(e).__name__}: {e}"}},
            ensure_ascii=False))
    finally:
        # 每次调用都是独立进程，退出前必须把浏览器子进程收干净，
        # 否则会在服务器上留下一堆 chromium 僵尸进程吃内存。
        #
        # 但收尾绝不能反过来拖垮整次调用：stdout 已经写完了（结果也拿到了），
        # 收尾卡住纯属白等。这里给 8 秒硬上限，超时就直接放弃清理，
        # 交给操作系统回收 —— 结果优先于整洁。
        try:
            await asyncio.wait_for(close_browser(), timeout=8.0)
        except asyncio.TimeoutError:
            log("浏览器清理超时（8s），直接退出")
        except Exception:
            pass
        else:
            log("浏览器已清理")

    # ★ 结果已落 stdout，立刻让解释器退出。
    #   asyncio.run() 在 return 后还会跑完 loop 的 asyncgen 清理和
    #   executor shutdown，那几样在带 Playwright driver 的进程里偶尔也会拖。
    #   这里 flush 完 stdout/stderr 直接 _exit，把退出时间钉死在「采集耗时」上。
    sys.stdout.flush()
    sys.stderr.flush()
    os._exit(0)


def main() -> None:
    raw = sys.stdin.read()
    try:
        req = json.loads(raw or "{}")
    except Exception:
        print(json.dumps({"ok": False, "error": {"code": "bad_json"}}))
        return

    storage = Storage(Path(req.get("storageDir") or "storage/tiktok"))
    asyncio.run(_serve(req, storage))


if __name__ == "__main__":
    main()
