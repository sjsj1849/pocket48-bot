# Instagram 监控

采集使用 [Instaloader](https://github.com/instaloader/instaloader) 4.15.3 的[模块接口](https://instaloader.github.io/as-module.html)，不调用发帖、点赞、关注或标记 Story 已读接口。

## 安装和配置

运行 `scripts/install_instagram_monitor.sh`，使用 Python 3 创建独立虚拟环境并安装锁定依赖。BOT 自动启动监控循环，配置存放 `storage/instagram/settings.json`，默认关闭。

现有配置页新增 Instagram 栏：运行开关、60–3600 秒检查间隔（默认 300 秒）、HTTP/SOCKS 代理、精确账号搜索（@用户名或主页链接）、按 QQ 群订阅、帖子/Reels/Story 开关、@全体和只读预览。总览一直显示服务入口；启用后显示实际扫描结果和最近活动。

在自己的浏览器登录 Instagram，导出 instagram.com 的 Cookie JSON 或 Cookie 请求头，使用配置栏的“验证并导入登录态”。至少需要 sessionid 和 csrftoken。服务器通过 test_login 验证成功后才替换会话，失败保留旧会话。API 不返回 Cookie，工作进程通过 stdin 接收候选，会话 JSON 原子写入、0600 权限、跨进程互斥。成功采集时保存响应更新的 Cookie，不使用密码或不可信 pickle。会话仍可能失效，需要再次导入。Story 和私密账号需登录且拥有查看资格。

## 转发和恢复

格式 `【账号|Instagram】`，caption、相册图片/视频封面、视频占位符、链接、空行、时间。QQ 视频另发，@全体独占一行。Feed 和 Reels 重复曝光同一媒体时仅保留一个事件。

首次订阅、账号更换、首次启用一种内容类型仅建立基线，不推送旧内容。使用媒体 ID 去重，Post/Reels 各自保存扫描时间；置顶旧帖不能提前结束扫描。分页未覆盖进度时扩大到 500 条，仍未覆盖就报错并保留进度，避免静默跳过。失败按连续次数延长间隔至最多 30 分钟。Story 超过平台保留期后无法补采。发送记录表示已进入 OneBot 队列，不保证 QQ 已读或实际送达。

## 验证与限制（2026-09-15）

Go 测试覆盖基线、持久去重、新增内容类型、账号变更、时间游标、QQ 媒体与 footer/@全体布局、面板会话脱敏。Python 测试覆盖混合相册顺序、Cookie 域名筛选、置顶和分页溢出、失败导入保留会话。

真实服务器对 @hearts2hearts 做未登录 Profile 请求，Instagram 返回 HTTP 429；已映射为 rate_limit，失败不是空列表。目前没有 Instagram 登录态，不能声称完成真实登录、Feed/Reels/Story 或 QQ 端实际投递验证。监控默认关闭，待导入用户登录态和添加账号后启用。Instagram 直播不在本轮范围。

## 账号密码和二次验证

配置栏同时支持用户名、邮箱或手机号/密码直接登录、六至八位二次验证码。密码只存在于本次请求和进程内存，不写会话文件；只有登录成功后才原子替换现有会话。2FA 待验证信息（不含密码）以 0600 保存，十分钟过期，成功或清除会话时删除。Instagram 安全 checkpoint 必须由账号本人在浏览器完成，完成后可导入 Cookie。当前服务器实际账号密码登录未成功，显示“服务器登录被拒绝”，不将其当作账号已登录。

服务器浏览器也尝试打开官方 accounts/login 表单，等待后未获得可操作的登录表单（TimeoutError）。用户名和手机号直接登录均返回 LoginException，尚无成功会话；不能把这个结果认定为密码错误。后续需在账号本人浏览器完成正常登录/安全验证后，通过配置栏导入 Cookie，再做真实采集验证。
