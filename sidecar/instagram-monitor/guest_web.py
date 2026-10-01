"""Low-cost public Instagram profile/feed reader based on the current web UI."""
import json
import re

import instaloader
import requests


PROFILE_POSTS_DOC_ID = "28570182382647478"


class GuestWebError(Exception):
    def __init__(self, code):
        self.code = code


def _find_key(value, key):
    if isinstance(value, dict):
        if key in value:
            return value[key]
        for child in value.values():
            found = _find_key(child, key)
            if found is not None:
                return found
    elif isinstance(value, list):
        for child in value:
            found = _find_key(child, key)
            if found is not None:
                return found
    return None


def _embedded_user(html):
    for raw in re.findall(r'<script[^>]+type=["\']application/json["\'][^>]*>(.*?)</script>', html, re.S | re.I):
        try:
            user = _find_key(json.loads(raw), "xig_user_by_username")
        except (TypeError, ValueError):
            continue
        if isinstance(user, dict) and user.get("username"):
            return user
    return None


def _request(session, method, url, **kwargs):
    try:
        response = session.request(method, url, timeout=20, **kwargs)
    except requests.exceptions.Timeout:
        raise GuestWebError("timeout") from None
    except requests.exceptions.RequestException:
        raise GuestWebError("account_unavailable") from None
    if response.status_code == 404:
        raise GuestWebError("user_unavailable")
    if response.status_code != 200:
        # PersistentLimiter handles 429/challenge before control reaches here.
        raise GuestWebError("account_unavailable")
    return response


def public_profile(session, username):
    response = _request(session, "GET", f"https://www.instagram.com/{username}/", headers={
        "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
        "Accept-Language": "en-US,en;q=0.9",
    })
    if "/accounts/login/" in response.url:
        raise GuestWebError("login_required")
    html = response.text
    profile_id = next(iter(re.findall(r'"profile_id":"(\d+)"', html)), None)
    lsd = next(iter(re.findall(r'"LSD",\[\],\{"token":"([^"]+)"', html)), None)
    user = _embedded_user(html) or {}
    profile_id = str(profile_id or user.get("id") or user.get("pk") or "")
    if not profile_id or not lsd:
        raise GuestWebError("guest_unavailable")
    actual = str(user.get("username") or username).lower()
    if actual != username.lower():
        raise GuestWebError("user_unavailable")
    return {
        "html": html, "lsd": lsd, "id": profile_id,
        "user": {
            "id": profile_id, "username": actual,
            "name": user.get("full_name") or actual,
            "avatar": user.get("profile_pic_url") or "",
            "protected": bool(user.get("is_private")),
        },
    }


def public_posts(session, username, page=None):
    page = page or public_profile(session, username)
    response = _request(session, "POST", "https://www.instagram.com/graphql/query", headers={
        "Accept": "*/*", "X-FB-LSD": page["lsd"],
        "X-IG-App-ID": "936619743392459", "X-ASBD-ID": "359341",
        "Referer": f"https://www.instagram.com/{username}/",
    }, data={
        "lsd": page["lsd"],
        "fb_api_req_friendly_name": "PolarisProfilePostsQuery",
        "doc_id": PROFILE_POSTS_DOC_ID,
        "variables": json.dumps({
            "data": {"count": 33, "include_reel_media_seen_timestamp": True,
                     "include_relationship_info": True, "latest_besties_reel_media": True,
                     "latest_reel_media": True},
            "username": username,
            "__relay_internal__pv__PolarisMultiCaptionCarouselEnabledrelayprovider": False,
            "__relay_internal__pv__PolarisReelsRecoDebugOverlayEnabledrelayprovider": False,
            "__relay_internal__pv__PolarisShortDramaEnabledrelayprovider": False,
        }, separators=(",", ":")),
    })
    try:
        payload = json.loads(response.text.removeprefix("for (;;);"))
        edges = payload["data"]["xdt_api__v1__feed__user_timeline_graphql_connection"]["edges"]
    except (KeyError, TypeError, ValueError):
        raise GuestWebError("guest_unavailable") from None
    if not isinstance(edges, list):
        raise GuestWebError("guest_unavailable")
    for edge in edges:
        item = edge.get("node") if isinstance(edge, dict) else None
        if not isinstance(item, dict):
            raise GuestWebError("scan_incomplete")
        owner = str((item.get("user") or {}).get("pk") or (item.get("owner") or {}).get("id") or "")
        collaborators = [str(x.get("pk") or x.get("id")) for x in item.get("coauthor_producers") or []]
        if owner and owner != page["id"] and page["id"] not in collaborators:
            raise GuestWebError("user_unavailable")
        # Keep the same raw shape consumed by raw_post_data.
        item = {**item, "has_liked": item.get("has_liked", False),
                "like_count": item.get("like_count", 0)}
        yield instaloader.Post.from_iphone_struct(None, item)
