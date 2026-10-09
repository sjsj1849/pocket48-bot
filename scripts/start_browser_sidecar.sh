#!/bin/bash
# 浏览器侧卡专用 Xvfb 桌面。
#
# 为什么不用 xvfb-run：本机上 llbot 长期占着 :99（640x480），
# xvfb-run -a 仍会挑到 :99，两个 X server 抢同一 display，
# 实际生效的是先启动的那个，于是 Chromium 1280 宽的窗口
# 被裁成左上角一小块，noVNC 里扫码二维码根本看不全。
# 这里显式占用一个没人用的 :77，与 llbot 完全隔离。
#
# 刻意不依赖 xdpyinfo（机器上没装 x11-utils），改用 X11 socket
# 文件 + 短暂等待来判断 Xvfb 是否就绪。
set -u

DISPLAY_NUM=77
export DISPLAY=":${DISPLAY_NUM}"
# admin 侧 findXDisplay() 同时要求 DISPLAY 与 XAUTHORITY，缺一个就当"没找到桌面"，
# 于是不会拉起 x0vncserver，面板里就只有一个空画布。
export XAUTHORITY="/tmp/.X${DISPLAY_NUM}-xvfb"
# 高度必须与 Chromium 的 --window-size=1280,720 对齐（index.mjs:677）。曾
# 是 1280x1024，多出的 304px 空白桌面让面板出现左右黑边 + 底部空白
SCREEN="1280x720x24"
SOCKET="/tmp/.X11-unix/X${DISPLAY_NUM}"

xvfb_alive() {
    [ -S "$SOCKET" ] || return 1
    # Xvfb 进程还在，且命令行里带着这个 display
    pgrep -f "Xvfb :${DISPLAY_NUM} " >/dev/null 2>&1
}

if ! xvfb_alive; then
    rm -f "/tmp/.X${DISPLAY_NUM}-lock" 2>/dev/null
    rm -f "$SOCKET" 2>/dev/null
    Xvfb "${DISPLAY}" -screen 0 "${SCREEN}" -nolisten tcp -ac >/tmp/xvfb77.log 2>&1 &
    for _ in $(seq 1 60); do
        if xvfb_alive; then
            break
        fi
        sleep 0.1
    done
fi

if ! xvfb_alive; then
    echo "Xvfb ${DISPLAY} 启动失败，见 /tmp/xvfb77.log" >&2
    exit 1
fi

exec node ./sidecar/weibo-auth/index.mjs "$@"
