# Instagram 监控

采集使用 [Instaloader](https://github.com/instaloader/instaloader) 4.15.3 的[模块接口](https://instaloader.github.io/as-module.html)，不调用发帖、点赞、关注或标记 Story 已读接口。

## 安装和配置

运行 `scripts/install_instagram_monitor.sh`，使用 Python 3 创建独立虚拟环境并安装锁定依赖。BOT 自动启动监控循环，配置存放 `storage/instagram/settings.json`，默认关闭。

现有配置页新增 Instagram 栏：运行开关、60–3600 秒检查间隔（默认 300 秒）、HTTP/SOCKS 代理、精确账号搜索（@用户名或主页链接）、按 QQ 群订阅、帖子/Reels/Story 开关、@全体和只读预览。总览一直显示服务入口；启用后显示实际扫描结果和最近活动。

在自己的浏览器登录 Instagram，导出 instagram.com 的 Cookie JSON 或 Cookie 请求头，使用配置栏的“验证并导入登录态”。至少需要 sessionid 和 csrftoken。服务器通过 test_login 验证成功后才替换会话，失败保留旧会话。API 不返回 Cookie，工作进程通过 stdin 接收候选，会话 JSON 原子写入、0600 权限、跨进程互斥。成功采集时保存响应更新的 Cookie，不使用密码或不可信 pickle。可在配置栏导入到内置浏览器，并自动同步浏览器 Cookie 的变化；会话仍可能失效，届时需要重新登录。Story 和私密账号需登录且拥有查看资格。

## 转发和恢复

格式 `【账号|Instagram】`，caption、相册图片/视频封面、视频占位符、链接、空行、时间。QQ 视频另发，@全体独占一行。Feed 和 Reels 重复曝光同一媒体时仅保留一个事件。

首次订阅、账号更换、首次启用一种内容类型仅建立基线，不推送旧内容。使用媒体 ID 去重，Post/Reels 各自保存扫描时间；置顶旧帖不能提前结束扫描。分页未覆盖进度时扩大到 500 条，仍未覆盖就报错并保留进度，避免静默跳过。失败按连续次数延长间隔至最多 30 分钟。Story 超过平台保留期后无法补采。发送记录表示已进入 OneBot 队列，不保证 QQ 已读或实际送达。

## 验证与限制（2026-09-15）

Go 测试覆盖基线、持久去重、新增内容类型、账号变更、时间游标、QQ 媒体与 footer/@全体布局、面板会话脱敏。Python 测试覆盖混合相册顺序、Cookie 域名筛选、置顶和分页溢出、失败导入保留会话。

真实服务器对 @hearts2hearts 做未登录 Profile 请求，Instagram 返回 HTTP 429；已映射为 rate_limit，失败不是空列表。后续用户导入 Cookie 已通过 test_login，账号精确搜索和一条普通多图帖子实际读取成功；Reels/Story 和 QQ 端实际投递仍待验证。监控默认关闭，待添加账号后启用。Instagram 直播不在本轮范围。

## 账号密码和二次验证

配置栏同时支持用户名、邮箱或手机号/密码直接登录、六至八位二次验证码。密码只存在于本次请求和进程内存，不写会话文件；只有登录成功后才原子替换现有会话。2FA 待验证信息（不含密码）以 0600 保存，十分钟过期，成功或清除会话时删除。Instagram 安全 checkpoint 必须由账号本人在浏览器完成，完成后可导入 Cookie。当前服务器实际账号密码登录未成功，显示“服务器登录被拒绝”，不将其当作账号已登录。

服务器浏览器也尝试打开官方 accounts/login 表单，等待后未获得可操作的登录表单（TimeoutError）。用户名和手机号直接登录均返回 LoginException，当时尚无成功会话；不能把这个结果认定为密码错误。后续已通过用户 Cookie 验证登录。


## 已登录资料查询修复

Instaloader 4.15.3 的 from_username 仍访问 web_profile_info，本服务器该路径返回 429。已登录时先使用库内 TopSearchResults 精确用户名查找，再由 Profile 的 GraphQL 元数据和 get_posts 读取；成功解析的稳定 ID 原子缓存，后续跳过搜索，加载元数据并检查 ID 和用户名一致，避免账号改名误读。关闭 iphone_support 和额外高清头像查询，使用帖子已提供的图片/视频 URL。Instaloader date_utc 是不带时区的 UTC 日期，统一显式设 UTC 再转毫秒，避免服务器时区导致偏移。

真实读取在少量验证后收到 HTTP 401 + “Please wait a few minutes before you try again”；这种响应也识别为 rate_limit，暂缓重试，不当作空列表或密码错误。有效会话保持私有存储，无凭据入库提交。


## 跨查询限流和帖子替代接口

所有采集器 Instagram HTTP 请求（含搜索、登录验证、帖子、Reels、Story）在发出之前记账。账号目录内的 `request-state.json` 保存近一小时请求、Instaloader 各查询类型的时间戳、冷却截止时间和连续限流次数；与会话共用进程锁，原子写入 0600。时间戳使用 epoch，重启进程/服务、重新登录、清除或导入 Cookie 都不清空预算。默认至少间隔 2 秒，并限制每分钟 12 次、每 10 分钟 60 次；这只是本地保守预算，不代表平台公开配额。

HTTP 429 或 401/403 带“wait a few minutes”会冻结所有采集请求，初次 15 分钟，重复失败逐次延长，最多 6 小时，同时尊重响应中的秒数 Retry-After。冷却时立即返回截止时间，不持续占用工作进程，也不会换接口绕过冷却。面板显示近一小时请求数和下一次允许请求时间。

借鉴 [Instaloader issue #2689](https://github.com/instaloader/instaloader/issues/2689) 验证 `/api/v1/feed/user/<uid>/`，保留相册、图片、视频和分页游标，并校验作者或合作作者 UID；响应结构改变或分页不完整时明确报错。GraphQL 硬错误可以回退；平台限流则保存下次尝试 v1 的偏好，先等冷却结束。此路径是社区方案，不能保证始终可用。

本服务器真实 v1 三次只读验证（分别遵守 15、30 分钟冷却）均收到“稍后再试”，目前尚未验证成功，第三次后保存 60 分钟冷却并停止自动验证。`probe-state.json` 保存待验证目标、次数和下一次重试；BOT 即使监控关闭也会在冷却后继续该明确安排的只读验证，最多三次实际尝试，不转发历史 QQ 消息。结果保留并显示在配置页。

## 内置浏览器同步

配置栏支持“导入到内置浏览器”“同步浏览器登录态”“打开 Instagram 浏览器”和刷新状态。启动浏览器时可从已验证的采集器会话恢复 Cookie；浏览器持久状态随侧卡存储。定时及 Instagram 响应后的同步只读取本地 Cookie，不自动访问平台保活。

浏览器 Cookie 变化先写入私有 `browser-candidate.json`，通过登录验证后才替换采集器已验证会话；冷却期间暂存，验证失败保留旧会话，拒绝的候选不会阻塞正常采集。浏览器退出登录也不覆盖旧采集器会话。导入或同步不会清除冷却，亦不人为延长 Cookie 的平台有效期。Instagram 标签页受后台清理保护，不能被抖音采集当作主标签页使用。


## 浏览器连接方式（2026-09-15）

参考 [instagram_monitor 3.4](https://github.com/misiektoja/instagram_monitor/blob/main/RELEASE_NOTES.md#changes-in-34-16-jun-2026) 的做法，Instagram 请求默认改用锁定版本 `curl_cffi`，使用固定 Chrome TLS/HTTP2 身份，并让 User-Agent 与连接身份一致。实现保留 Requests 的请求准备、Cookie 域名/路径判断和跳转流程，关闭底层隐藏重试；每个实际跳转仍先进入持久请求预算。代理、超时、证书校验及响应 Cookie 均继续生效。其他平台和媒体 CDN 不经过该适配器。

这只能减少 Linux 默认 TLS 特征导致的误拦截，不能解除账号或出口 IP 已存在的限制。当前冷却不会被提前清除。冷却结束后执行一次同账号、同出口的少量对照：先调用浏览器连接方式；若它失败，立即停止并跳过原方式；若它成功，才以原 Requests 方式读取同一 feed，各一次并保存脱敏结果，不发送 QQ 消息。验证使用独立的一次尝试，不重置原有请求记录。实际浏览器连接方式仍收到平台限流，因此按规则跳过原方式对照并保存结果；这说明当前限制并非只由 Python 默认 TLS 特征造成。
