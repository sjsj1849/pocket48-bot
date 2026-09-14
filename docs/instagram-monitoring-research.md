# Instagram 监控选型（2026-09-15）

结论：可以接入现有 BOT。建议以 Instaloader 的 Python 模块为采集器，沿用 X 的独立采集进程、订阅存储、游标、OneBot 媒体投递和面板健康状态。后续已安装并接入 BOT，配置栏和服务总览见 [接入说明](instagram-integration.md)。真实未登录请求收到 429；尚未提供 Instagram 登录态，所以默认关闭，真实登录及内容采集待验证。

## 候选项目

| 项目 | 已确认能力 | 本项目建议用途 |
| --- | --- | --- |
| [Instaloader](https://github.com/instaloader/instaloader) / [模块文档](https://instaloader.github.io/as-module.html) | 图片、视频、多图混合帖、文本 caption、Reels、Story；可导入浏览器 Cookie 并保存会话 | 主采集器，使用 Post ID/shortcode 持久去重，逐项保留相册顺序 |
| [instagrapi](https://github.com/subzeroid/instagrapi) | Web/Mobile API、图文/视频/相册/Reels/Story、sessionid 登录、2FA 和邮件/SMS challenge 回调 | 登录或接口兼容问题时的备选；目前只需读内容，不引入其发帖/互动能力 |
| [yt-dlp Instagram extractor](https://github.com/yt-dlp/yt-dlp/blob/master/yt_dlp/extractor/instagram.py) | 单帖/视频提取，登录 Cookie 支持和网页提取回退 | 视频解析备选，不用它承担整个账号的新帖监控 |

这些项目提供采集能力，不是可直接替换本 BOT 的完整提醒系统。仍需实现面板设置、持久去重、事件格式和 QQ 投递。

## 接入设计

- 配置支持用户名、主页链接精确搜索及账号确认；Instagram 昵称搜索能否稳定可用须登录后验证，不能照搬 X 的保证。
- 每个订阅选择图文帖子、Reels、Story、推送群和 @全体。第一轮只建基线；相同 ID 不重发，置顶/合作帖按 ID 去重。
- QQ 第一条发 `【账号|Instagram】`、caption（可选翻译）、按原顺序排列的图片/视频封面/视频占位符；最后放链接、空行、时间戳。MP4 独立发送，与其他平台一致。
- 总览显示是否启用、登录态、最近成功检查、错误及最近活动。失败不能伪装成“暂无新帖”；验证码/登录挑战进入需处理状态。
- 采集账号串行或受限并发；建议初始 5–10 分钟轮询、超时与指数退避，按真实账号数量与接口表现调整。Story 用单独游标，过期 Story 不能保证补采。
- 公共账号可尝试未登录抓取，但为了持续运行建议导入浏览器登录态并自动保存更新 Cookie。私密账号需要登录账号拥有查看权限。保存会话不等于永不过期；平台仍可能要求重新验证。

以上架构、间隔和格式是根据现有项目设计作出的工程建议，并非开源库保证。正式接入前需确定首批 Instagram 用户名并用真实会话做只读验证，重点检查普通帖、多图混合帖、Reels、Story 和限流后的恢复。Instagram 直播提醒不在本轮已验证能力中。
