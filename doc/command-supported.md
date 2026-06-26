# 机器人支持的命令列表

所有命令默认前缀为 `bot` (可在 `config.json` 中修改)。

## 基础命令

### 1. 开启监控 (`on`)
启动消息监听和转发功能。

*   **格式**: `<前缀> on`
*   **示例**: `bot on`
*   **适用范围**: QQ 群 (Admin)

### 2. 关闭监控 (`off`)
停止消息监听和转发功能。

*   **格式**: `<前缀> off`
*   **示例**: `bot off`
*   **适用范围**: QQ 群 (Admin)

### 3. 调试模式 (`debug`)
切换调试模式，输出更多日志信息。

*   **格式**: `<前缀> debug <on/off>`
*   **示例**: `bot debug on`
*   **适用范围**: QQ 群 (Admin)

### 4. 登录 (`login`)
使用 Token 或 账号密码 登录 Pocket48。
*建议优先在 `config.json` 配置好，此命令用于紧急更新 Token*。

*   **格式**:
    *   Token: `<前缀> login <token>`
    *   密码: `<前缀> login <手机号> <密码>`
    *   验证码触发: `<前缀> login by code <手机号>` (仅 SuperAdmin)
*   **示例**:
    *   `bot login aBcDe...`
    *   `bot login by code 13800138000` (发送验证码)
*   **适用范围**: 私聊 (Admin)

### 4.1 验证码登录 (`code`)
当使用 `login by code` 触发验证码发送后，或自动触发时，使用此命令提交验证码。

*   **格式**: `<前缀> code <验证码>`
*   **示例**: `bot code 123456`
*   **适用范围**: 私聊 (Super Admin)
*   **触发条件**: 机器人启动或 CheckToken 失败时会自动触发 SMS 发送。

---

## 订阅管理

### 5. 搜索房间 (`search`)
此命令用于查找小偶像的频道信息（Channel ID）。包含频道名称和ID。

* 1.  **搜索**: 使用 `bot search <名字>` 查找小偶像的频道 (Channel) 和 ID。
2.  **监控**: 在目标 QQ 群发送 `bot monitor <ChannelID>` 或 `bot monitor all <名字>`。
3.  **开始**: 机器人将开始转发该频道的消息。
*   **功能**:
    *   根据名字搜索小偶像。
    *   返回服务器 ID 和 房间 ID (RoomID)。

### 6. 获取房间ID (`getRoomID`)
通过 StarID (需通过抓包获取) 获取房间 ID。通常建议使用 `search`。

*   **格式**: `<前缀> getRoomID <starID>`
*   **示例**: `bot getRoomID 12345`
*   **适用范围**: QQ 群 / 私聊

## 4. 监控管理

### 4.1 添加监控 (`monitor`)
添加需要监控的频道 (Channel) 到当前群组。

*   **格式**:
    *   单个频道: `<前缀> monitor <ChannelID>`
    *   所有频道: `<前缀> monitor all <小偶像名字>`
*   **示例**:
    *   `bot monitor 123456`
    *   `bot monitor all 林舒晴` (自动添加林舒晴所有下属频道)
*   **适用范围**: QQ 群 (Admin)

### 4.2 移除监控 (`remove`)
移除当前群组已监控的频道。

*   **格式**: `<前缀> remove <ChannelID>`
*   **示例**: `bot remove 123456`
*   **适用范围**: QQ 群 (Admin)

### 4.3 列表查询 (`list`)
查看当前群组正在监控的频道。

*   **格式**:
    *   `bot list` (显示监控列表)
    *   `bot list channels` (显示具体频道名称)
*   **适用范围**: QQ 群

### 4.4 绑定群组 (`bind`) (New)
设置或修改机器人绑定的 QQ 群。只有 Super Admin 有权操作。

*   **格式**: `bot bind`
*   **示例**: `bot bind` (在目标群发送)
*   **适用范围**: QQ 群 (Super Admin)

---

## 管理员管理

### 10. 管理员操作 (`admin`)
添加或移除机器人管理员。此命令仅限 `SUPER_ADMIN` (在 config.json 设置) 使用。

*   **格式**: `<前缀> admin <add/remove> <QQ号>`
*   **示例**: `bot admin add 12345678`
*   **适用范围**: QQ 群 / 私聊 (Super Admin)

---

## 高级设置

### 11. 设置轮询间隔 (`interval`)
设置机器人请求 Pocket48 接口的频率。

*   **格式**: `<前缀> interval [set <秒数>]`
*   **示例**:
    *   `bot interval`: 查看当前间隔。
    *   `bot interval set 3`: 设置为 3 秒。
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **功能**:
    *   动态调整机器人检查新消息的速度。
    *   设置过快可能导致 API 限制，建议保持在 1-5 秒之间。

### 12. 初始消息获取窗口 (`window`)
控制机器人启动或初次监听某个房间时，获取多久以前的历史消息。默认为 60 分钟。

*   **格式**: `<前缀> window [set <分钟数>]`
*   **示例**:
    *   `bot window`: 查看当前窗口大小。
    *   `bot window set 30`: 设置为 30 分钟。
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **功能**:
    *   防止重启机器人时刷屏大量旧消息。

### 13. 直播通知开关 (`live`)
单独控制是否推送直播通知（LivePush）。可独立于消息监听开启或关闭。支持全局控制或指定房间控制。

*   **格式**: `<前缀> live <on/off> [RoomID / 小偶像名字]`
*   **示例**:
    *   `bot live`: 查看当前状态及用法。
    *   `bot live on`: 全局开启所有房间的直播通知。
    *   `bot live off`: 全局关闭所有房间的直播通知。
    *   `bot live off 67248386`: 仅关闭房间 67248386 的直播通知 (ID模式)。
    *   `bot live on 林舒晴`: 仅开启林舒晴房间的直播通知 (名字搜索模式)。
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **优先级**: 指定房间的设置优于全局设置。
*   **功能**:
    *   灵活控制直播通知推送，例如只想要房间消息而不想要直播通知时使用。
    *   可以针对特定成员开启或关闭强提醒。

### 14. 礼物回复开关 (`gift`)
控制是否显示成员回复礼物的消息（GIFTREPLY）。默认关闭。

*   **格式**: `<前缀> gift <on/off> [RoomID / 小偶像名字]`
*   **示例**:
    *   `bot gift on 67248386`: 开启该房间的礼物回复显示。
    *   `bot gift off 林舒晴`: 关闭该房间的礼物回复显示。
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **默认值**: 关闭 (不显示礼物回复)。

### 14.1 年度青春盛典记分开关 (`score`)
控制是否显示年度青春盛典计分礼物（GIFT_TEXT 且 `giftInfo.isScore=1`），推送内容会突出积分。

*   **格式**: `<前缀> score <on/off> [RoomID / 小偶像名字]`
*   **示例**:
    *   `bot score on 1279287`: 开启该房间的年度记分推送。
    *   `bot score off 胡晓慧`: 关闭该房间的年度记分推送。
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **默认值**: 关闭。

### 14.2 归档运维 (`archive`)
查看归档状态与手动重试归档队列。

*   **格式**:
    *   `<前缀> archive status`
    *   `<前缀> archive retry`
*   **示例**:
    *   `bot archive status`
    *   `bot archive retry`
*   **适用范围**: QQ 群 / 私聊 (Admin)

### 15. NIM 实时监听 (`nim`)
控制 NIM sidecar 的实时直播监听能力。

*   **格式**:
    *   `<前缀> nim status`
    *   `<前缀> nim on`
    *   `<前缀> nim off`
    *   `<前缀> nim restart`
    *   `<前缀> nim account <口袋账号>`
    *   `<前缀> nim mode <auto|im|anon>`
    *   `<前缀> nim fallback <on/off>`
    *   `<前缀> nim cross <on/off>`
    *   `<前缀> nim roommsg <on/off>`
    *   `<前缀> nim watch <on/off>`
    *   `<前缀> nim online <on/off>`
    *   `<前缀> nim online-notify <on/off>`
    *   `<前缀> nim gifts`
    *   `<前缀> nim gifts <房间ID|成员名>`
    *   `<前缀> nim gifts <房间ID|成员名> <开始时间> <结束时间>`
*   **示例**:
    *   `bot nim status`
    *   `bot nim on`
    *   `bot nim mode auto`
    *   `bot nim fallback on`
    *   `bot nim cross on`
    *   `bot nim roommsg on`
    *   `bot nim watch on`
    *   `bot nim online on`
    *   `bot nim online-notify on`
    *   `bot nim gifts`
    *   `bot nim gifts 67248386`
    *   `bot nim gifts 林舒晴 2026-04-09T20:00 2026-04-09T20:30`
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **说明**:
    *   `nim on` 启动实时监听；`nim off` 关闭实时监听但不影响轮询兜底。
    *   `nim mode auto`：优先 IM；遇到 NIM 鉴权失败时可回退匿名入房（受 fallback 开关控制）。
    *   `nim mode im`：仅使用 IM 登录链路，不做匿名回退。
    *   `nim mode anon`：跳过 IM 登录，直接匿名入房。
    *   `nim fallback on/off`：控制 auto 模式是否允许匿名回退。
    *   `nim cross on` 开启“小偶像在他人直播间发言”推送（基于昵称匹配）。
    *   `nim roommsg on/off` 控制“直播间普通文本消息”的实时转发（默认关闭；轮询链路继续作为兜底）。
    *   `nim watch on/off` 控制“正在看/离开直播间/观看时长”通知（默认关闭）。
    *   `nim online on/off` 控制上线/离线事件采集（实验性，默认关闭）。
    *   `nim online-notify on/off` 控制是否把上线/离线事件推送到群（实验性，默认关闭）。

### 15.1 微博超话签到 (`weibo super`)
管理当前群超话列表、手动签到和自动签到。

*   **格式**:
    *   `<前缀> weibo super list`
    *   `<前缀> weibo super add <oid> [名称]`
    *   `<前缀> weibo super del <oid|名称>`
    *   `<前缀> weibo super sign [all|oid|名称]`
    *   `<前缀> weibo super auto <on/off>`
*   **示例**:
    *   `bot weibo super add 1022:100808f5f84f0f5f322e17d053b5b8f5a2cbf7 超话名`
    *   `bot weibo super sign all`
    *   `bot weibo super auto on`
*   **适用范围**: QQ 群 / 私聊 (Admin)
*   **返回语义**:
    *   `100000`: 签到成功
    *   `382004`: 今日已签到
    *   `382010`: 超话不存在或不可见

### 16. 昵称管理 (`nickname`)
管理小偶像的昵称。添加昵称后，暂用于记录和查询，未来可能用于模糊搜索。

*   **格式**:
    *   添加: `<前缀> nickname add <名字> <昵称>`
    *   删除: `<前缀> nickname del <名字> <昵称>`
    *   列表: `<前缀> nickname <名字>`
*   **示例**:
    *   `bot nickname add 林舒晴 十八选一姐`
    *   `bot nickname 林舒晴` (显示所有昵称)
*   **适用范围**: QQ 群 / 私聊 (Admin)

### 16. 昵称反查 (`who`)
通过昵称查找对应的小偶像（所有者）。

*   **格式**: `<前缀> who <昵称>`
*   **示例**: `bot who 十八选一姐`
*   **返回**: `昵称 [十八选一姐] 属于：【林舒晴】`
*   **适用范围**: QQ 群 / 私聊
---

## 常见问题 (FAQ)

### Q: 为什么添加监控后没有消息？
A:
1. 请确认机器人账号已登录 (`login` 命令)。
2. 请确认该房间在其 APP 内有新消息产生。
3. 请确认日志中是否有报错信息。
