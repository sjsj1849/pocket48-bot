"""Normalize twscrape models without sending messages or updating BOT cursors."""
import html
import re
from datetime import datetime, timezone
from urllib.parse import urlparse


def username(value):
    value = value.strip()
    if "://" in value:
        link = urlparse(value)
        if link.scheme != "https" or link.hostname not in {"x.com", "www.x.com", "twitter.com", "www.twitter.com"}:
            raise ValueError("invalid_username")
        parts = link.path.strip("/").split("/")
        if len(parts) != 1:
            raise ValueError("invalid_username")
        value = parts[0]
    value = value.removeprefix("@")
    if not re.fullmatch(r"[a-zA-Z0-9_]{1,15}", value):
        raise ValueError("invalid_username")
    return value


def user(data):
    return {"id": str(data.get("id_str") or data["id"]), "username": data["username"],
            "name": data["displayname"], "avatar": data.get("profileImageUrl", ""),
            "protected": bool(data.get("protected")), "pinnedIds": [str(i) for i in data.get("pinnedIds", [])]}


def https(value):
    return value if isinstance(value, str) and urlparse(value).scheme == "https" else ""


def raw_media_order(payload):
    """Index original attachment order before twscrape groups media by type."""
    result = {}
    def visit(node):
        if isinstance(node, dict):
            identity = str(node.get("rest_id") or node.get("id_str") or "")
            legacy = node.get("legacy") or node
            entries = (legacy.get("extended_entities") or {}).get("media") or []
            if identity and entries:
                result[identity] = [m.get("media_url_https", "") for m in entries]
            for child in node.values():
                visit(child)
        elif isinstance(node, list):
            for child in node:
                visit(child)
    visit(payload)
    return result


def primary_tweet_ids(payload):
    """Select displayed tweets without promoting nested quotes/reposts to events."""
    result = set()
    def visit(node):
        if isinstance(node, dict):
            if node.get('__typename') == 'Tweet':
                identity = str(node.get('rest_id') or '')
                if identity:
                    result.add(identity)
                return
            for key, child in node.items():
                if key not in {'quoted_status_result', 'retweeted_status_result'}:
                    visit(child)
        elif isinstance(node, list):
            for child in node:
                visit(child)
    visit(payload)
    return result


def event(data, orders=None, depth=0):
    if depth > 2:
        return None
    orders = orders or {}
    identity = str(data.get("id_str") or data["id"])
    date = data["date"]
    if isinstance(date, str):
        date = datetime.fromisoformat(date)
    if date.tzinfo is None:
        date = date.replace(tzinfo=timezone.utc)
    author = user(data["user"])
    body = html.unescape(data.get("rawContent", ""))
    for link in data.get("links") or []:
        if link.get("tcourl") and https(link.get("url")):
            body = body.replace(link["tcourl"], link["url"])
    media = data.get("media") or {}
    attachments = []
    for photo in media.get("photos") or []:
        source = https(photo.get("url"))
        if source:
            attachments.append({"kind": "image", "url": source, "cover": source})
    for video in media.get("videos") or []:
        variants = [{"url": https(v.get("url")), "bitrate": v.get("bitrate", 0)}
                    for v in video.get("variants") or [] if v.get("contentType") == "video/mp4" and https(v.get("url"))]
        variants.sort(key=lambda v: v["bitrate"], reverse=True)
        attachments.append({"kind": "video", "cover": https(video.get("thumbnailUrl")),
                            "variants": variants, "durationMS": video.get("duration", 0)})
    for animated in media.get("animated") or []:
        source = https(animated.get("videoUrl"))
        attachments.append({"kind": "animated", "cover": https(animated.get("thumbnailUrl")),
                            "variants": [{"url": source, "bitrate": 0}] if source else []})
    order = orders.get(identity, [])
    positions = {url: i for i, url in enumerate(order)}
    attachments.sort(key=lambda a: positions.get(a.get("cover"), len(order)))
    retweet = data.get("retweetedTweet")
    quote = data.get("quotedTweet")
    kind = "repost" if retweet else "reply" if data.get("inReplyToTweetIdStr") else "post"
    if quote and kind == "post":
        kind = "quote"
    return {"id": identity, "kind": kind, "author": author, "body": body,
            "url": f"https://x.com/{author['username']}/status/{identity}",
            "time": int(date.timestamp() * 1000), "media": attachments,
            "replyToId": str(data.get("inReplyToTweetIdStr") or ""),
            "reposted": event(retweet, orders, depth + 1) if retweet else None,
            "quoted": event(quote, orders, depth + 1) if quote else None,
            "mediaOrderKnown": bool(order) or len(attachments) < 2}
