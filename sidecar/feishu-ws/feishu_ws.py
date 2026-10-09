# -*- coding: utf-8 -*-
"""飞书长连接 sidecar（官方 lark-oapi SDK）。

★ 为什么不用手写 WebSocket 协议（2026-10-04）：
  先手写了一版（Go 侧 internal/outbound/feishu_ws.go），连续踩坑：
    1. 猜错app_ticket / app_access_token 路径 → 都 404；
    2. 猜错 code 字段类型（飞书成功时返回数字，解到 int 报错）；
    3. 取端点返回 code=9499 Bad Request —— 这条是**后台还没切长连接模式**，
       不是代码问题，但说明手写协议在错误面前没有辨识力。
  协议细节（端点、握手、心跳、重连）交给 SDK。

★ 一个绕不过去的前提（官方文档原文）：
  「选择『使用长连接接收事件』，并保存
    （**必须本地客户端启动正常，有长连接在线的情况下才能保存成功**）」
  ⇒ 首次配置流程：先让本进程跑起来连上一次，再去后台保存长连接模式。

★ SDK 实测细节（都是踩出来的，不要凭印象改）：
  - `EventDispatcherHandler.builder(verification_token, encrypt_key)` 要**两个**参数；
  - `register_p2_im_message_receive_v1` **只有一个**，私聊和群消息都走它，
    靠 `event.event.message.chat_type` 区分（p2p / group）；
  - 字段路径是 `event.event.message` / `event.event.sender`，
    sender_id 是 `UserId` 对象，取 `.open_id`。

协议：stdout 持续输出事件行；读 stdin 仅用于探活。
"""
import json
import os
import sys
import threading
import time

APP_ID = os.environ.get("FEISHU_APP_ID", "")
APP_SECRET = os.environ.get("FEISHU_APP_SECRET", "")

# 长连接状态，供探活时回报。
_state = {"connected": False, "events": 0, "started_at": 0.0, "error": ""}


def _log(msg: str) -> None:
    print(f"[feishu-ws] {msg}", file=sys.stderr, flush=True)


def _emit(payload: dict) -> None:
    _state["events"] += 1
    print(json.dumps({"type": "event", "event": payload}, ensure_ascii=False),
          flush=True)


def _sender_id(data) -> str:
    try:
        uid = data.sender.sender_id
        return getattr(uid, "open_id", "") or getattr(uid, "user_id", "") or ""
    except Exception:
        return ""


def _on_message(event) -> None:
    """私聊与群消息的统一入口。"""
    try:
        data = event.event
        msg = data.message
        if msg is None:
            return
        # 非文本一律不处理（图片/文件/分享卡片暂不支持）。
        if msg.message_type != "text":
            return

        # content 是 JSON 字符串："{\"text\":\"...\"}"
        try:
            text = json.loads(msg.content or "{}").get("text", "")
        except Exception:
            text = msg.content or ""
        if not text.strip():
            return

        chat_type = msg.chat_type or "p2p"
        mention_bot = False
        # 剔除 @ 占位符。飞书把 @ 渲染成「@_user_1」这样的 key。
        for mn in (msg.mentions or []):
            key = getattr(mn, "key", "") or ""
            if key:
                mention_bot = True
                text = text.replace(key, "")
        text = text.strip()

        if chat_type != "p2p" and not mention_bot:
            return  # 群里没 @ 就不理
        if not text:
            return

        _log(f"收到消息 chat_type={chat_type} mention={mention_bot} text={text[:100]!r}")
        _emit({
            "chatType": "p2p" if chat_type == "p2p" else "group",
            "chatId": msg.chat_id or "",
            "messageId": msg.message_id or "",
            "senderId": _sender_id(data),
            "text": text,
            "mentionBot": mention_bot,
        })
    except Exception as exc:  # noqa: BLE001
        _log(f"处理消息失败: {type(exc).__name__}: {exc}")


def main() -> int:
    if not APP_ID or not APP_SECRET:
        print(json.dumps({"type": "error",
                          "error": "missing FEISHU_APP_ID/FEISHU_APP_SECRET"}),
              flush=True)
        return 0

    from lark_oapi import EventDispatcherHandler, ws

    handler = (EventDispatcherHandler.builder("", "")
               .register_p2_im_message_receive_v1(_on_message)
               .build())

    client = ws.Client(APP_ID, APP_SECRET, event_handler=handler)

    _state["started_at"] = time.time()
    _log(f"正在建立长连接 appID={APP_ID}")

    def _run() -> None:
        try:
            client.start()
        except Exception as exc:  # noqa: BLE001
            _state["error"] = f"{type(exc).__name__}: {exc}"
            _log(f"长连接异常退出: {_state['error']}")

    t = threading.Thread(target=_run, daemon=True)
    t.start()

    # 探活线程：SDK 的 client.start() 是阻塞的，连接状态只能从
    # 线程存活 + 有无异常推断。connected 表示「进程已正常跑起来」。
    def _watch() -> None:
        time.sleep(8)
        _state["connected"] = _state["error"] == ""
        if _state["connected"]:
            _log("长连接已就绪（若后台未开长连接模式，这里仍拿不到事件）")
    threading.Thread(target=_watch, daemon=True).start()

    # stdin 只做探活：每读一行回一行状态。
    # ★ 事件走「主动写 stdout」而不是「读到请求才回」——
    #   链接提取是异步的（解析+下载数十秒），不能等 stdin 才有响应。
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        if line == "ping":
            print(json.dumps({
                "type": "pong",
                "connected": _state["connected"],
                "events": _state["events"],
                "uptime": round(time.time() - _state["started_at"], 1),
                "error": _state["error"],
            }, ensure_ascii=False), flush=True)
        elif line == "quit":
            break

    return 0


if __name__ == "__main__":
    sys.exit(main() or 0)