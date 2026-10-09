#!/usr/bin/env python3
"""Fetch Douyin author posts/details with the current native signing stack.

The A-Bogus implementation is vendored from Evil0ctal/Douyin_TikTok_Download_API
v5.1.2 (Apache-2.0), whose bdms.js and webSign reverse engineering was live
validated in September 2026. Input and output are one JSON document on stdio.
"""

from __future__ import annotations

import hashlib
import json
import secrets
import sys
import time
from pathlib import Path
from urllib.parse import quote

from curl_cffi import requests

VENDOR = Path(__file__).resolve().parent / "vendor" / "evil0ctal-douyin"
sys.path.insert(0, str(VENDOR))

from abogus import ABogus, browser_info_from_screen  # noqa: E402


USER_AGENT = (
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
    "(KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)
WEBSIGN_SALT = "A96D855A08C0A9707F8BEF0D9A527E4E"
UIFID_NAMES = ("uifid", "uifid_temp", "uifidtemp", "UIFID", "UIFID_TEMP", "UIFIDTEMP")


def encode_pairs(pairs: list[tuple[str, str]]) -> str:
    return "&".join(f"{quote(str(k), safe='*-._')}={quote(str(v), safe='*-._')}" for k, v in pairs)


def first_url(value: object) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, list) and value:
        return str(value[0] or "")
    if isinstance(value, dict):
        urls = value.get("url_list") or value.get("urlList") or []
        if isinstance(urls, list) and urls:
            return str(urls[0] or "")
    return ""


def image_url(image: object) -> str:
    if not isinstance(image, dict):
        return first_url(image)
    for key in (
        "watermark_free_download_url_list",
        "download_url_list",
        "url_list",
        "download_url",
        "display_image",
        "owner_watermark_image",
    ):
        url = first_url(image.get(key))
        if url:
            return url
    return first_url(image)


def _codec_family(variant: dict) -> str:
    """从 variant 里挖出编码族，返回 "h264" / "hevc" / "unknown"。

    抖音各版本字段名不统一：可能是 codec_type，也塞在 format /
    quality / extra 里，所以全部看一眼再下结论。**认不出就返回
    unknown，绝不猜** —— 猜错会导致选到 hevc 而以为自己是 h264。
    """
    candidates = []
    for key in ("codec_type", "codec", "format", "quality", "quality_type", "extra"):
        value = variant.get(key)
        if isinstance(value, str):
            candidates.append(value.lower())
        elif isinstance(value, dict):
            for inner in value.values():
                if isinstance(inner, str):
                    candidates.append(inner.lower())
    blob = " ".join(candidates)
    if "h265" in blob or "hevc" in blob:
        return "hevc"
    if "h264" in blob or "avc" in blob:
        return "h264"
    return "unknown"


def best_video_variant(video: object) -> tuple[str, int, str]:
    """返回 (url, bitrate, codec)。

    排序键用 (family_rank, bitrate) 字典序：只要存在 h264 档就一定
    排在所有 hevc 档之前；h264 档之间再比码率。全部 unknown 时
    等价于原来的「纯取最高码率」，不引入新的行为偏差。
    """
    if not isinstance(video, dict):
        return "", 0, ""
    best_url = ""
    best_key = None
    best_rate = 0
    best_family = ""
    for variant in video.get("bit_rate") or []:
        if not isinstance(variant, dict):
            continue
        url = first_url(variant.get("play_addr"))
        if not url:
            continue
        try:
            rate = int(variant.get("bit_rate") or 0)
        except (TypeError, ValueError):
            rate = 0
        family = _codec_family(variant)
        rank = {"h264": 0, "unknown": 1, "hevc": 2}[family]
        key = (rank, rate)
        if best_key is None or key > best_key:
            best_url, best_key, best_rate, best_family = url, key, rate, family
    if best_url:
        return best_url, max(best_rate, 0), best_family
    return first_url(video.get("play_addr")), 0, ""


def highest_bitrate_video(video: object) -> tuple[str, int]:
    """保留旧签名，内部委托给 best_video_variant。"""
    url, rate, _ = best_video_variant(video)
    return url, rate


def normalize_item(item: dict, fallback_sec_user_id: str = "") -> dict:
    author = item.get("author") or {}
    aweme_id = str(item.get("aweme_id") or "")
    raw_images = item.get("images") or (item.get("image_post_info") or {}).get("images") or []
    images = [image_url(image) for image in raw_images]
    images = [url for url in images if url]
    live_photo_videos = []
    for image in raw_images:
        if not isinstance(image, dict):
            continue
        live_url, _ = highest_bitrate_video(image.get("video"))
        if live_url and live_url not in live_photo_videos:
            live_photo_videos.append(live_url)
    video = item.get("video") or {}
    video_url, video_bitrate, video_codec = best_video_variant(video)
    cover = (
        first_url(video.get("cover"))
        or first_url(video.get("origin_cover"))
        or first_url(video.get("dynamic_cover"))
        or (images[0] if images else "")
    )
    kind = "note" if images or int(item.get("aweme_type") or 0) == 68 else "video"
    return {
        "id": aweme_id,
        "secUserId": str(author.get("sec_uid") or fallback_sec_user_id),
        "nickname": str(author.get("nickname") or ""),
        "desc": str(item.get("desc") or item.get("title") or "").strip(),
        "createTime": int(item.get("create_time") or 0),
        "type": kind,
        "url": str(item.get("share_url") or f"https://www.douyin.com/{kind}/{aweme_id}"),
        "cover": cover,
        "images": images,
        "videoUrl": video_url if kind == "video" else "",
        # ★ 时长（秒）2026-10-05 新增：跨平台去重要在**下载前**判定，
        #   而 download 前唯一的时长来源就是这个字段（video.duration 毫秒）。
        #   之前没往外传，只能下载后 ffprobe，判定晚了两分钟。
        "duration": (round(int(video.get("duration") or 0) / 1000)
                     if kind == "video" else 0),
        "videoBitrate": video_bitrate if kind == "video" else 0,
        "videoCodec": video_codec if kind == "video" else "",
        "livePhotoVideos": live_photo_videos,
    }


def normalize_posts(body: dict, sec_user_id: str) -> list[dict]:
    posts: list[dict] = []
    seen: set[str] = set()
    for item in body.get("aweme_list") or []:
        author = item.get("author") or {}
        if str(author.get("sec_uid") or "") != sec_user_id:
            continue
        aweme_id = str(item.get("aweme_id") or "")
        if not aweme_id or aweme_id in seen:
            continue
        seen.add(aweme_id)
        posts.append(normalize_item(item, sec_user_id))
    return posts


def signed_query(resource_pairs: list[tuple[str, str]], cookies: dict[str, str]) -> tuple[str, dict[str, str]]:
    pairs = [
        ("device_platform", "webapp"),
        ("aid", "6383"),
        ("channel", "channel_pc_web"),
        ("pc_client_type", "1"),
        ("version_code", "290100"),
        ("version_name", "29.1.0"),
        ("cookie_enabled", "true"),
        ("screen_width", "1920"),
        ("screen_height", "1080"),
        ("browser_language", "zh-CN"),
        ("browser_platform", "Win32"),
        ("browser_name", "Chrome"),
        ("browser_version", "136.0.0.0"),
        ("browser_online", "true"),
        ("engine_name", "Blink"),
        ("engine_version", "136.0.0.0"),
        ("os_name", "Windows"),
        ("os_version", "10"),
        ("cpu_core_num", "12"),
        ("device_memory", "8"),
        ("platform", "PC"),
        ("downlink", "10"),
        ("effective_type", "4g"),
        ("round_trip_time", "0"),
        ("update_version_code", "170400"),
        *resource_pairs,
    ]
    ms_token = cookies.get("msToken")
    if ms_token:
        pairs.append(("msToken", ms_token))
    query = encode_pairs(pairs)
    a_bogus = ABogus(
        USER_AGENT,
        browser_info=browser_info_from_screen(1920, 1080, "Win32"),
    ).get_value(query)
    pairs.append(("a_bogus", a_bogus))
    verify_fp = cookies.get("s_v_web_id")
    if verify_fp:
        pairs.extend((("verifyFp", verify_fp), ("fp", verify_fp)))
    uifid = next((cookies.get(name) for name in UIFID_NAMES if cookies.get(name)), "")
    if not uifid:
        raise ValueError("Douyin UIFID cookie is missing")
    pairs.extend((("uifid", uifid), ("timestamp", str(int(time.time())))))
    covered = encode_pairs(pairs)
    signature = hashlib.md5(
        f"{uifid}_{pairs[-1][1]}_{WEBSIGN_SALT}_{covered}".encode()
    ).hexdigest()
    return f"{covered}&x-secsdk-web-signature={signature}", {
        "uifid": uifid,
        "x-secsdk-web-signature": signature,
        "x-secsdk-web-expire": pairs[-1][1],
    }


def fetch_one(session: requests.Session, sec_user_id: str, cookies: dict[str, str], proxy: str) -> dict:
    try:
        query, signing_headers = signed_query([
            ("sec_user_id", sec_user_id),
            ("max_cursor", "0"),
            ("count", "20"),
            ("publish_video_strategy_type", "2"),
            ("from_user_page", "1"),
            ("locate_query", "false"),
            ("need_time_list", "1"),
            ("show_live_replay_strategy", "1"),
            ("time_list_query", "0"),
            ("pc_libra_divert", "Windows"),
            ("whale_cut_token", ""),
        ], cookies)
        response = session.get(
            f"https://www.douyin.com/aweme/v1/web/aweme/post/?{query}",
            headers={
                "User-Agent": USER_AGENT,
                "Referer": "https://www.douyin.com/",
                "Origin": "https://www.douyin.com",
                "Accept": "application/json, text/plain, */*",
                **signing_headers,
            },
            cookies=cookies,
            proxy=proxy or None,
            impersonate="chrome136",
            timeout=20,
            allow_redirects=False,
        )
        try:
            body = response.json()
        except Exception:
            preview = response.text[:160].replace("\n", " ")
            return {
                "secUserId": sec_user_id,
                "http": response.status_code,
                "status_code": -1,
                "message": f"non-json response bytes={len(response.content)} preview={preview}",
                "posts": [],
            }
        status_code = int(body.get("status_code", -1))
        return {
            "secUserId": sec_user_id,
            "http": response.status_code,
            "status_code": status_code,
            "message": str(body.get("status_msg") or body.get("message") or ""),
            "posts": normalize_posts(body, sec_user_id) if status_code == 0 else [],
        }
    except Exception as error:
        return {
            "secUserId": sec_user_id,
            "http": 0,
            "status_code": -1,
            "message": str(error),
            "posts": [],
        }


def fetch_detail(session: requests.Session, aweme_id: str, cookies: dict[str, str], proxy: str) -> dict:
    try:
        query, signing_headers = signed_query([("aweme_id", aweme_id)], cookies)
        response = session.get(
            f"https://www.douyin.com/aweme/v1/web/aweme/detail/?{query}",
            headers={
                "User-Agent": USER_AGENT,
                "Referer": f"https://www.douyin.com/video/{aweme_id}",
                "Origin": "https://www.douyin.com",
                "Accept": "application/json, text/plain, */*",
                **signing_headers,
            },
            cookies=cookies,
            proxy=proxy or None,
            impersonate="chrome136",
            timeout=20,
            allow_redirects=False,
        )
        body = response.json()
        status_code = int(body.get("status_code", -1))
        item = body.get("aweme_detail") if status_code == 0 else None
        return {
            "awemeId": aweme_id,
            "http": response.status_code,
            "status_code": status_code,
            "message": str(body.get("status_msg") or body.get("message") or ""),
            "post": normalize_item(item) if isinstance(item, dict) else None,
        }
    except Exception as error:
        return {"awemeId": aweme_id, "http": 0, "status_code": -1, "message": str(error), "post": None}


def main() -> None:
    request = json.load(sys.stdin)
    cookies = {str(k): str(v) for k, v in (request.get("cookies") or {}).items() if v}
    proxy = str(request.get("proxy") or "")
    session = requests.Session()
    results = []
    values = request.get("awemeIds") if request.get("mode") == "detail" else request.get("secUserIds")
    for index, value in enumerate(values or []):
        if index:
            time.sleep(0.5 + secrets.randbelow(250) / 1000)
        if request.get("mode") == "detail":
            results.append(fetch_detail(session, str(value), cookies, proxy))
        else:
            results.append(fetch_one(session, str(value), cookies, proxy))
    json.dump(results, sys.stdout, ensure_ascii=False, separators=(",", ":"))


if __name__ == "__main__":
    main()
