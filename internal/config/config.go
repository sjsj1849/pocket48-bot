package config

import (
	"time"

	"encoding/json"
	"os"
	"strings"
)

type Config struct {
	MessageGatewayEnabled       bool                                       `json:"MESSAGE_GATEWAY_ENABLED"`
	MessageGatewayURL           string                                     `json:"MESSAGE_GATEWAY_URL"`
	MessageGatewayAPIKey        string                                     `json:"MESSAGE_GATEWAY_API_KEY"`
	QQEnabled                   bool                                       `json:"QQ_ENABLED"`
	NapCatWSURL                 string                                     `json:"NAPCAT_WS_URL"`
	NapCatAccessToken           string                                     `json:"NAPCAT_ACCESS_TOKEN"`
	FeishuEnabled               bool                                       `json:"FEISHU_ENABLED"`
	FeishuAppID                 string                                     `json:"FEISHU_APP_ID"`
	FeishuAppSecret             string                                     `json:"FEISHU_APP_SECRET"`
	FeishuGroupMap              map[string]string                          `json:"FEISHU_GROUP_MAP"` // QQ group id -> Feishu chat_id
	FeishuGroupRoutes           string                                     `json:"FEISHU_GROUP_ROUTES,omitempty"`
	FeishuPrivateRoutes         string                                     `json:"FEISHU_PRIVATE_ROUTES,omitempty"`
	FeishuUploadConcurrency     int                                        `json:"FEISHU_UPLOAD_CONCURRENCY"`
	PocketUsername              string                                     `json:"POCKET_USERNAME"`
	PocketPassword              string                                     `json:"POCKET_PASSWORD"`
	PocketToken                 string                                     `json:"POCKET_TOKEN"`
	NIMToken                    string                                     `json:"NIM_TOKEN"`
	AdminQQ                     []int64                                    `json:"ADMIN_QQ"` // Changed to array of int64
	SuperAdmin                  int64                                      `json:"SUPER_ADMIN"`
	BoundGroupID                int64                                      `json:"BOUND_GROUP_ID"`
	CommandPrefix               string                                     `json:"COMMAND_PREFIX"`
	GroupSubscriptions          map[string][]int64                         `json:"GROUP_SUBSCRIPTIONS"`  // GroupID (string) -> List of RoomIDs
	InitialFetchWindow          int64                                      `json:"INITIAL_FETCH_WINDOW"` // Minutes
	LiveMonitoring              bool                                       `json:"LIVE_MONITORING"`
	LiveSpecific                map[string]bool                            `json:"LIVE_SPECIFIC"`         // RoomID (string) -> bool
	GiftSpecific                map[string]bool                            `json:"GIFT_SPECIFIC"`         // RoomID (string) -> bool
	AnnualScoreSpecific         map[string]bool                            `json:"ANNUAL_SCORE_SPECIFIC"` // RoomID (string) -> bool
	NIMEnabled                  bool                                       `json:"NIM_ENABLED"`
	NIMSidecarCmd               string                                     `json:"NIM_SIDECAR_CMD"`
	NIMAccount                  string                                     `json:"NIM_ACCOUNT"`
	NIMRoomMessageEnabled       bool                                       `json:"NIM_ROOM_MESSAGE_ENABLED"`
	NIMRoomMessagePollFallback  bool                                       `json:"NIM_ROOM_MESSAGE_POLL_FALLBACK"`
	NIMLiveDanmakuEnabled       bool                                       `json:"NIM_LIVE_DANMAKU_ENABLED"`
	NIMViewerEventEnabled       bool                                       `json:"NIM_VIEWER_EVENT_ENABLED"`
	PocketMemberFeatures        map[string]*PocketMemberFeatureConfig      `json:"POCKET_MEMBER_FEATURES,omitempty"`
	PollingInterval             int                                        `json:"POLLING_INTERVAL"`                         // Seconds
	LastStartupTime             int64                                      `json:"LAST_STARTUP_TIME"`                        // Unix Timestamp
	WeiboSubscriptions          map[int64]map[string]*WeiboConfig          `json:"WEIBO_SUBSCRIPTIONS"`                      // GroupID -> UID -> WeiboConfig
	WeiboSuperPostSubscriptions map[int64]map[string]*WeiboSuperPostConfig `json:"WEIBO_SUPERPOST_SUBSCRIPTIONS"`            // GroupID -> key(uid|oid) -> config
	WeiboSuperTopics            map[int64]map[string]*WeiboSuperTopic      `json:"WEIBO_SUPER_TOPICS"`                       // GroupID -> OID -> Topic
	WeiboSuperAutoEnabled       bool                                       `json:"WEIBO_SUPER_AUTO_ENABLED"`                 // Daily auto super-topic sign-in
	WeiboSuperLastRunDate       string                                     `json:"WEIBO_SUPER_LAST_RUN_DATE"`                // YYYY-MM-DD
	WeiboSuperCountEnabled      bool                                       `json:"WEIBO_SUPER_COUNT_ENABLED"`                // Enable weibo super count feature
	WeiboSuperCountTopics       map[string]*WeiboSuperCountTopic           `json:"WEIBO_SUPER_COUNT_TOPICS"`                 // OID -> Topic for count feature
	WeiboSuperCountGroups       map[string]*WeiboSuperCountGroupInfo       `json:"WEIBO_SUPER_COUNT_GROUPS"`                 // group_id -> group info
	WeiboSuperCountImageGroups  map[string]*WeiboSuperCountImageGroupInfo  `json:"WEIBO_SUPER_COUNT_IMAGE_GROUPS,omitempty"` // image_id -> groups combined in one PNG
	// WeiboSuperCountDelivery: email | qq | both (default both when empty)
	WeiboSuperCountDelivery string `json:"WEIBO_SUPER_COUNT_DELIVERY,omitempty"`
	// WeiboSuperCountQQ: extra QQ numbers (private) for daily report when delivery includes qq
	WeiboSuperCountQQ string `json:"WEIBO_SUPER_COUNT_QQ,omitempty"`
	// WeiboReportImageTargets: target ids the daily PNG attachments fan out to.
	WeiboReportImageTargets           []string                                           `json:"WEIBO_REPORT_IMAGE_TARGETS,omitempty"`
	WeiboSuperCountLastPushDate       string                                             `json:"WEIBO_SUPER_COUNT_LAST_PUSH_DATE"`     // YYYY-MM-DD (Asia/Shanghai)
	WeiboSuperCountDailySnapshots     map[string]map[string]int                          `json:"WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS"`    // YYYY-MM-DD -> OID -> SignCount
	WeiboSuperCountDailySnapshotsV2   map[string]map[string]*WeiboSuperCountSnapshotItem `json:"WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS_V2"` // YYYY-MM-DD -> OID -> SnapshotItem
	WeiboAppAuthInvalidLastNotifyDate string                                             `json:"WEIBO_APP_AUTH_INVALID_LAST_NOTIFY_DATE,omitempty"`
	// WeiboSignMonitor 打开「超话签到分钟级监测」。见 logic/weibo_sign_monitor.go。
	WeiboSignMonitor                WeiboSignMonitorConfig               `json:"WEIBO_SIGN_MONITOR,omitempty"`
	WeiboAppAuthHealthCheckNotifyAt string                               `json:"WEIBO_APP_AUTH_HEALTH_CHECK_NOTIFY_AT,omitempty"` // 主动健康检查上次通知时间 (unix ts)
	WeiboApp                        *WeiboAppConfig                      `json:"WEIBO_APP,omitempty"`
	BilibiliSubscriptions           map[int64]map[string]*BilibiliConfig `json:"BILIBILI_SUBSCRIPTIONS"`        // GroupID -> RoomID -> BilibiliConfig
	WeiboCookie                     string                               `json:"WEIBO_COOKIE"`                  // Weibo web Cookie
	WeiboMWeiboCookie               string                               `json:"WEIBO_MWEIBO_COOKIE,omitempty"` // mweibo.com / m.weibo.cn Cookie
	WeiboBrowserAuthEnabled         bool                                 `json:"WEIBO_BROWSER_AUTH_ENABLED"`
	WeiboBrowserAuthCmd             string                               `json:"WEIBO_BROWSER_AUTH_CMD"`
	WeiboBrowserProfileDir          string                               `json:"WEIBO_BROWSER_PROFILE_DIR"`
	WeiboBrowserHeadless            bool                                 `json:"WEIBO_BROWSER_HEADLESS"`
	WeiboBrowserRefreshMinutes      int                                  `json:"WEIBO_BROWSER_REFRESH_MINUTES"`
	BrowserSidecarCmd               string                               `json:"BROWSER_SIDECAR_CMD"`
	BrowserProfileDir               string                               `json:"BROWSER_PROFILE_DIR"`
	BrowserHeadless                 bool                                 `json:"BROWSER_HEADLESS"`
	// BrowserProxyServer: optional Chromium proxy for the shared weibo-auth browser
	// (Playwright proxy.server). Example: http://127.0.0.1:17890
	// Used to bypass datacenter IP bans (e.g. Xiaohongshu 300012 / HTTP 461).
	BrowserProxyServer         string                             `json:"BROWSER_PROXY_SERVER,omitempty"`
	DouyinEnabled              bool                               `json:"DOUYIN_ENABLED"`
	DouyinPollSeconds          int                                `json:"DOUYIN_POLL_SECONDS"`
	DouyinLiveWSURL            string                             `json:"DOUYIN_LIVE_WS_URL"`
	DouyinLiveSidecarCmd       string                             `json:"DOUYIN_LIVE_SIDECAR_CMD"`
	DouyinLiveSummaryEnabled   bool                               `json:"DOUYIN_LIVE_SUMMARY_ENABLED"`
	DouyinLiveSoundWaveEnabled bool                               `json:"DOUYIN_LIVE_SOUND_WAVE_ENABLED"`
	DouyinLiveRawStatsDebug    bool                               `json:"DOUYIN_LIVE_RAW_STATS_DEBUG"`
	DouyinLiveRawGiftDebug     bool                               `json:"DOUYIN_LIVE_RAW_GIFT_DEBUG"`
	DouyinLiveCookieAccount    string                             `json:"DOUYIN_LIVE_COOKIE_KEYRING_ACCOUNT,omitempty"`
	DouyinSubscriptions        map[int64]map[string]*DouyinConfig `json:"DOUYIN_SUBSCRIPTIONS"`

	// TiktokSubscriptions TikTok 账号名 -> 配置。
	// 不带群组层：TikTok 侧只监控固定几个账号，群组维度用不上。
	TiktokSubscriptions map[string]*TiktokConfig `json:"TIKTOK_SUBSCRIPTIONS,omitempty"`

	// TiktokEnabled TikTok（洋抖）作品监控总开关。
	// 与 DouyinEnabled 相互独立 —— 两者采集链路、限流特征、UA 要求全都不同，
	// 绑在一起会造成意料外的连带停机。
	TiktokEnabled             bool                                    `json:"TIKTOK_ENABLED"`
	DouyinIMEnabled           bool                                    `json:"DOUYIN_IM_ENABLED"`
	DouyinIMPrivateEnabled    bool                                    `json:"DOUYIN_IM_PRIVATE_ENABLED"`
	DouyinIMGroupName         string                                  `json:"DOUYIN_IM_GROUP_NAME"`
	DouyinIMGroupNumber       string                                  `json:"DOUYIN_IM_GROUP_NUMBER"`
	XiaohongshuEnabled        bool                                    `json:"XIAOHONGSHU_ENABLED"`
	XiaohongshuPollSeconds    int                                     `json:"XIAOHONGSHU_POLL_SECONDS"`
	XiaohongshuSubscriptions  map[int64]map[string]*XiaohongshuConfig `json:"XIAOHONGSHU_SUBSCRIPTIONS"`
	DeliveryTargets           []DeliveryTarget                        `json:"DELIVERY_TARGETS"`
	AlertEmailEnabled         bool                                    `json:"ALERT_EMAIL_ENABLED"`
	AlertEmailTo              string                                  `json:"ALERT_EMAIL_TO"`
	AlertEmailFrom            string                                  `json:"ALERT_EMAIL_FROM"`
	AlertEmailCooldownMinutes int                                     `json:"ALERT_EMAIL_COOLDOWN_MINUTES"`
	AlertEmailSMTPHost        string                                  `json:"ALERT_EMAIL_SMTP_HOST"`
	AlertEmailSMTPPort        int                                     `json:"ALERT_EMAIL_SMTP_PORT"`
	AlertEmailSMTPUser        string                                  `json:"ALERT_EMAIL_SMTP_USER"`
	AlertEmailSMTPPassword    string                                  `json:"ALERT_EMAIL_SMTP_PASSWORD"`
	AdminPanelURL             string                                  `json:"ADMIN_PANEL_URL"`
	MediaDelivery             string                                  `json:"MEDIA_DELIVERY"`         // local | remote
	DisableGroupCommands      bool                                    `json:"DISABLE_GROUP_COMMANDS"` // Disable command handling in groups
	WelcomeConfigs            map[int64]*WelcomeConfig                `json:"WELCOME_CONFIGS"`        // GroupID -> WelcomeConfig
	WeidianOrders             map[int64]*WeidianOrderConfig           `json:"WEIDIAN_ORDERS"`         // GroupID -> WeidianOrderConfig
	filePath                  string

	// baselineFields 保存 LoadConfig 时从磁盘读到的原值。
	// Save() 用它判断「本次进程是否真的改过某字段」：没改过的字段
	// 一律保留磁盘现值。长期驻留的结构体不再把加载时的旧值写回磁盘，
	// 也就不会回滚掉面板/脚本在运行期写入的新值。
	baselineFields map[string]json.RawMessage
}

type PocketMemberFeatureConfig struct {
	VisitNotify bool `json:"visit_notify"`
	KeepHistory bool `json:"keep_history"`
}

// FeishuRoutes merges structured routes with the panel-friendly
// "QQ group=Feishu chat_id" list.
func (c *Config) FeishuRoutes() map[string]string {
	routes := make(map[string]string, len(c.FeishuGroupMap))
	for source, destination := range c.FeishuGroupMap {
		if strings.TrimSpace(source) != "" && strings.TrimSpace(destination) != "" {
			routes[strings.TrimSpace(source)] = strings.TrimSpace(destination)
		}
	}
	for _, line := range strings.FieldsFunc(c.FeishuGroupRoutes, func(r rune) bool {
		return r == '\n' || r == ',' || r == ';'
	}) {
		source, destination, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(source) != "" && strings.TrimSpace(destination) != "" {
			routes[strings.TrimSpace(source)] = strings.TrimSpace(destination)
		}
	}
	return routes
}

// FeishuPrivateRouteMap maps legacy QQ administrator IDs to Feishu open_id.
func (c *Config) FeishuPrivateRouteMap() map[string]string {
	routes := make(map[string]string)
	for _, line := range strings.FieldsFunc(c.FeishuPrivateRoutes, func(r rune) bool {
		return r == '\n' || r == ',' || r == ';'
	}) {
		source, destination, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(source) != "" && strings.TrimSpace(destination) != "" {
			routes[strings.TrimSpace(source)] = strings.TrimSpace(destination)
		}
	}
	return routes
}

type WeiboConfig struct {
	UID       string   `json:"uid"`
	TargetIDs []string `json:"targetIds,omitempty"`
	Name      string   `json:"name,omitempty"` // screen_name / 昵称，面板展示用
	AtAll     bool     `json:"at_all"`
	LastID    string   `json:"last_id,omitempty"`
}

// DouyinConfig stores one creator subscription. SecUserID is the stable key
// used by creator pages; LiveID is the live.douyin.com path discovered from
// the creator page and can be reused while the creator is offline.
type DouyinConfig struct {
	SecUserID     string   `json:"sec_user_id"`
	TargetIDs     []string `json:"targetIds,omitempty"`
	ProfileURL    string   `json:"profile_url,omitempty"`
	Name          string   `json:"name,omitempty"`
	NameManual    bool     `json:"name_manual,omitempty"`
	AtAll         bool     `json:"at_all"`
	LastAwemeID   string   `json:"last_aweme_id,omitempty"`
	LastAwemeTime int64    `json:"last_aweme_time,omitempty"`
	LiveID        string   `json:"live_id,omitempty"`
	Auto          bool     `json:"auto,omitempty"`
	Disabled      bool     `json:"disabled,omitempty"`
	WorksDisabled bool     `json:"works_disabled,omitempty"`
	LiveDisabled  bool     `json:"live_disabled,omitempty"`
}

// TiktokConfig 是一个 TikTok（洋抖）账号的监控配置。
//
// 刻意只保留真正需要的字段，不复用 DouyinConfig：
// 后者有 SecUserID/LiveID/aweme 游标等一堆 TikTok 用不上的字段，
// 照抄一份会变成两个几乎相同、却各自漂移的结构体。
//
// 结构体刻意比 DouyinConfig 小很多：TikTok 侧只有动态监控，
// 没有 IM、没有直播，游标由 sidecar 的 state.json 自行维护（不占 config）。
type TiktokConfig struct {
	// Username TikTok 用户名（不含 @），如 "hearts2hearts"。
	Username string `json:"username,omitempty"`
	// DisplayName 面板上显示的名字，留空则用 Username。
	DisplayName string `json:"display_name,omitempty"`
	// TargetIDs 投递目标 ID 列表，语义与其它平台一致。
	TargetIDs []string `json:"targetIds,omitempty"`
	// Disabled 临时停用该账号，配置仍保留。
	Disabled bool `json:"disabled,omitempty"`
}

// XiaohongshuConfig stores a creator subscription and its per-group cursor.
// UserID is Xiaohongshu's internal profile ID, not the user-facing Red ID.
type XiaohongshuConfig struct {
	UserID          string   `json:"user_id"`
	TargetIDs       []string `json:"targetIds,omitempty"`
	ProfileURL      string   `json:"profile_url,omitempty"`
	Name            string   `json:"name,omitempty"`
	AtAll           bool     `json:"at_all"`
	LastNoteID      string   `json:"last_note_id,omitempty"`
	LastNoteTime    int64    `json:"last_note_time,omitempty"`
	LiveInitialized bool     `json:"live_initialized,omitempty"`
	LiveActive      bool     `json:"live_active,omitempty"`
}

type WeiboSuperTopic struct {
	OID            string `json:"oid"`
	Name           string `json:"name,omitempty"`
	LastSignDate   string `json:"last_sign_date,omitempty"`
	LastSignStatus string `json:"last_sign_status,omitempty"`
	LastSignRank   int    `json:"last_sign_rank,omitempty"` // 签到排名 = 今日总签到数
}

type WeiboSuperPostConfig struct {
	UID        string `json:"uid"`
	OID        string `json:"oid"`
	Name       string `json:"name,omitempty"`
	AtAll      bool   `json:"at_all"`
	LastPostID string `json:"last_post_id,omitempty"`
}

type WeiboSuperCountGroupInfo struct {
	Name string `json:"name"` // Display name for the group
}

// WeiboSignMonitorConfig 配置超话签到的分钟级监测。
//
// 默认值在 logic 侧（cfg.*() 方法）兜底，这里留空也能跑：
// 分组默认「八小妹」、间隔 5 分钟、窗口 30 分钟、绝对阈值 800 / 相对 25%。
type WeiboSignMonitorConfig struct {
	Enabled  bool   `json:"enabled"`
	GroupKey string `json:"groupKey,omitempty"`
	// IntervalMinutes 采样间隔（分钟）。
	IntervalMinutes int `json:"intervalMinutes,omitempty"`
	// RetentionHours 采样明细保留时长（小时）。
	RetentionHours int `json:"retentionHours,omitempty"`
	// SpikeWindowMinutes 涨幅检测窗口（分钟）。
	SpikeWindowMinutes int `json:"spikeWindowMinutes,omitempty"`
	// SpikeAbsolute 单窗口内绝对涨幅阈值（人）。
	SpikeAbsolute int `json:"spikeAbsolute,omitempty"`
	// SpikeRatio 单窗口内相对涨幅阈值（0.25 = 25%）。
	SpikeRatio float64 `json:"spikeRatio,omitempty"`
	// SpikeCooldownMinutes 同一尖峰的重复告警冷却（分钟）。
	SpikeCooldownMinutes int `json:"spikeCooldownMinutes,omitempty"`
	// AlertTargets 告警投递目标；为空时回落到 WEIBO_REPORT_IMAGE_TARGETS。
	AlertTargets []string `json:"alertTargets,omitempty"`
	// Groups 把监测范围内的超话按量级分组。
	//
	// ★ 为什么必须分组（2026-10-07 用户反馈）：
	//   「绝对阈值不合理」。实测八小妹 8 个超话当天签到从 3271 到 12000 不等，
	//   同一个绝对阈值对 3000 档是"暴涨"、对 12000 档只是日常波动。
	//   真正刺眼的是**某一条明显高于同组其他人**。
	Groups []WeiboSignMonitorGroup `json:"groups,omitempty"`
	// SpikeGroupRatio 组内异常倍数：某超话窗口涨幅 > 同组中位数 × 该倍数即异常。
	SpikeGroupRatio float64 `json:"spikeGroupRatio,omitempty"`
	// SpikeGroupMinDelta 组内判据的最小绝对增量，滤掉小基数噪声。
	SpikeGroupMinDelta int `json:"spikeGroupMinDelta,omitempty"`
	// DailyReportEnabled 每天定时发一封汇总报表（HTML 正文 + 图表 PNG + 原始 CSV）。
	DailyReportEnabled bool `json:"dailyReportEnabled,omitempty"`
	// DailyReportHour / DailyReportMinute 每天的发送时刻（本地时间）。
	DailyReportHour   int `json:"dailyReportHour,omitempty"`
	DailyReportMinute int `json:"dailyReportMinute,omitempty"`
	// SpikeReportEnabled 检出异常时立刻发一封（带异常时段柱状图 + CSV）。
	SpikeReportEnabled bool `json:"spikeReportEnabled,omitempty"`

	//★ 以下为 2026-10-08 面板化新增：让「发到哪、要不要发」都能在面板配置。

	// AnomalyReportEnabled异常时是否推送。
	//
	// ★ 与 DailyReportEnabled 分开：日报每天都要发（不管有没有异常），
	//   异常推送只在想告警时才发 —— 两者不能共用一个开关。
	AnomalyReportEnabled bool `json:"anomalyReportEnabled,omitempty"`

	// EmailReportEnabled 是否发邮件（日报 + 异常都受它控制）。
	EmailReportEnabled bool `json:"emailReportEnabled,omitempty"`

	// EmailTo 收件人，可填多个（逗号分隔由调用方拆）。
	EmailTo string `json:"emailTo,omitempty"`

	// ImageTargets 图片扇出去向（飞书/QQ 的 target id，可多选）。
	//
	// ★ 与 AlertTargets 的区别：AlertTargets 收的是**纯文字**告警，
	//   这个收的是**图片**（日报图 + 异常补充图）。
	//   留空时回落到 WEIBO_REPORT_IMAGE_TARGETS（与历史行为兼容）。
	ImageTargets []string `json:"imageTargets,omitempty"`

	// CrossoverSuspectRatio 破万者「疑似异常」的速度倍数（相对其自身基线）。
	//
	// ★ 破万后拿不到精确数据，不能直接判异常，只能看速度：
	//   拿它破万前自己的平均小时速度当基线，模糊期隐含速度超过基线这么多倍
	//   才标「疑似」。速度正常则完全不报 —— 用户明确要求。
	CrossoverSuspectRatio float64 `json:"crossoverSuspectRatio,omitempty"`

	// CrossoverSuspectRate 破万后整千跳变的**绝对**小时速度门槛（人/小时）。
	//
	// ★ 这是主判据（2026-10-08 用户纠正）：「异常发生的那一段本身太快」，
	//   不该被它自己临破万前的冲榜节奏带偏。只看相对基线会漏判
	//   （实测郑伊安那次相对基线只有 2.66 倍，卡在 3 以下）。
	CrossoverSuspectRate float64 `json:"crossoverSuspectRate,omitempty"`
}

// WeiboSignMonitorGroup 一组同量级的超话（按名字引用，避免 oid 写死在配置里）。
type WeiboSignMonitorGroup struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// 下面是默认值兜底：配置留空时也能跑出一套合理参数，
// 免得「enabled=true 但没配间隔」时退化成 0 间隔把接口打爆。
const (
	weiboSignMonitorDefaultInterval  = 5 * time.Minute
	weiboSignMonitorDefaultRetention = 72 * time.Hour
	weiboSignMonitorDefaultWindowMin = 30
	// ★ 阈值从 800 改成 150（2026-10-08）。
	//
	// 800 是拍出来的：实测 10-07 全天 1912 个采样点，第一组
	// 「单步增量 - 同组中位数」的分布是 p50=4 / p90=14 / p99=70 / max=567。
	// **正常波动的上界只有 70，而门槛是 800 —— 比真实刷量还高，
	// 所以告警从来没有触发过一次。**
	// 150 = p99 的约 2 倍，既能抓住 +351/+564 的连续刷量，误报又极少。
	weiboSignMonitorDefaultSpikeAbs  = 150
	weiboSignMonitorDefaultSpikeRate = 0.25
	weiboSignMonitorDefaultSpikeCool = 60 * time.Minute
	weiboSignMonitorMinInterval      = time.Minute
	weiboSignMonitorMaxInterval      = 6 * time.Hour
	// 破万者疑似异常的速度倍数（相对其破万前的自身基线）。
	weiboSignMonitorDefaultSuspect = 3.0
	// 破万后整千跳变的绝对小时速度门槛（人/小时）。
	//
	// ★ 1000 的来历（2026-10-08 真实数据校准）：8 个超话 29 小时采样里，
	//   未破万超话全天平均只有 12~34 人/小时（凌晨低峰），活跃时段
	//   也就「一小时几百」；而郑伊安那次 40 分钟涨 1000 = 1494 人/小时。
	//   用户原话：「按照正常情况来说，一个小时涨个几百才是正常的」。
	weiboSignMonitorDefaultFuzzyRate = 1000.0
)

// Interval 采样间隔，夹在 1 分钟 ~ 6 小时之间。
func (c WeiboSignMonitorConfig) Interval() time.Duration {
	if c.IntervalMinutes <= 0 {
		return weiboSignMonitorDefaultInterval
	}
	d := time.Duration(c.IntervalMinutes) * time.Minute
	if d < weiboSignMonitorMinInterval {
		return weiboSignMonitorMinInterval
	}
	if d > weiboSignMonitorMaxInterval {
		return weiboSignMonitorMaxInterval
	}
	return d
}

// Retention 采样明细保留时长（下限 6 小时）。
func (c WeiboSignMonitorConfig) Retention() time.Duration {
	if c.RetentionHours <= 0 {
		return weiboSignMonitorDefaultRetention
	}
	d := time.Duration(c.RetentionHours) * time.Hour
	if d < 6*time.Hour {
		return 6 * time.Hour
	}
	return d
}

// SpikeWindow 涨幅检测窗口（分钟）。
func (c WeiboSignMonitorConfig) SpikeWindow() int {
	if c.SpikeWindowMinutes <= 0 {
		return weiboSignMonitorDefaultWindowMin
	}
	return c.SpikeWindowMinutes
}

// SpikeAbs 单窗口绝对涨幅阈值（人）。
func (c WeiboSignMonitorConfig) SpikeAbs() int {
	if c.SpikeAbsolute <= 0 {
		return weiboSignMonitorDefaultSpikeAbs
	}
	return c.SpikeAbsolute
}

// SpikeRate 单窗口相对涨幅阈值（方法名避开同名字段 SpikeRatio）。
func (c WeiboSignMonitorConfig) SpikeRate() float64 {
	if c.SpikeRatio <= 0 {
		return weiboSignMonitorDefaultSpikeRate
	}
	return c.SpikeRatio
}

// SpikeGroupRatio 组内异常倍数（默认 2：比同组中位数高一倍）。
func (c WeiboSignMonitorConfig) GroupSpikeRatio() float64 {
	if c.SpikeGroupRatio <= 0 {
		return 2
	}
	return c.SpikeGroupRatio
}

// GroupSpikeMinDelta 组内判据的最小绝对增量（默认 300）。
func (c WeiboSignMonitorConfig) GroupSpikeMinDelta() int {
	if c.SpikeGroupMinDelta <= 0 {
		return 300
	}
	return c.SpikeGroupMinDelta
}

// CrossoverSuspectRatio 破万者疑似异常的速度倍数（默认 3倍基线）。
func (c WeiboSignMonitorConfig) CrossoverSuspect() float64 {
	if c.CrossoverSuspectRatio <= 0 {
		return weiboSignMonitorDefaultSuspect
	}
	return c.CrossoverSuspectRatio
}

// CrossoverFuzzyRate 破万后整千跳变的绝对小时速度门槛（默认 1000 人/小时）。
func (c WeiboSignMonitorConfig) CrossoverFuzzyRate() float64 {
	if c.CrossoverSuspectRate <= 0 {
		return weiboSignMonitorDefaultFuzzyRate
	}
	return c.CrossoverSuspectRate
}

// DailyReportAt 返回每天发送报表的时刻。
func (c WeiboSignMonitorConfig) DailyReportAt() (hour, minute int) {
	hour, minute = 23, 50
	if c.DailyReportHour >= 0 && c.DailyReportHour <= 23 {
		hour = c.DailyReportHour
	}
	if c.DailyReportMinute >= 0 && c.DailyReportMinute <= 59 {
		minute = c.DailyReportMinute
	}
	return hour, minute
}

// GroupNames 返回配置里的分组名（用于面板下拉）。
func (c WeiboSignMonitorConfig) GroupNames() []string {
	out := make([]string, 0, len(c.Groups))
	for _, g := range c.Groups {
		if name := g.Name; name != "" {
			out = append(out, name)
		}
	}
	return out
}

// SpikeCooldown 同一尖峰重复告警的冷却时间。
func (c WeiboSignMonitorConfig) SpikeCooldown() time.Duration {
	if c.SpikeCooldownMinutes <= 0 {
		return weiboSignMonitorDefaultSpikeCool
	}
	return time.Duration(c.SpikeCooldownMinutes) * time.Minute
}

// WeiboSuperCountImageGroupInfo describes one daily-report PNG attachment.
// GroupKeys references keys in WEIBO_SUPER_COUNT_GROUPS. An empty config keeps
// the legacy behaviour: all report groups are rendered into one image.
type WeiboSuperCountImageGroupInfo struct {
	Name      string   `json:"name"`
	GroupKeys []string `json:"group_keys"`
	// TargetIDs are the delivery targets this specific PNG fans out to. Empty
	// means "fall back to the global WEIBO_REPORT_IMAGE_TARGETS list", so
	// existing setups keep working after the upgrade.
	TargetIDs []string `json:"target_ids,omitempty"`
}

type WeiboSuperCountTopic struct {
	OID        string `json:"oid"`
	Name       string `json:"name,omitempty"`
	ReportSign int    `json:"report_sign,omitempty"` // 0=正常自动签到, 1=随日报签到拿精确排名, >1=连续精确天数
	GroupName  string `json:"group_name,omitempty"`  // which group this topic belongs to
}

type WeiboSuperCountSnapshotItem struct {
	Name               string `json:"name,omitempty"`
	SignCount          int    `json:"sign_count"`
	SuperLikeCount     int    `json:"super_like_count"`
	SuperLikeKnown     bool   `json:"super_like_known,omitempty"`
	Heat24h            string `json:"heat24h,omitempty"`
	ReadCount          string `json:"read_count,omitempty"`
	PostCount          string `json:"post_count,omitempty"`
	FansCount          string `json:"fans_count,omitempty"`
	LevelText          string `json:"level_text,omitempty"`
	CreatorOfficerText string `json:"creator_officer_text,omitempty"`
	FanDiamondText     string `json:"fan_diamond_text,omitempty"`
	DailyRankText      string `json:"daily_rank_text,omitempty"`
	CheckinExpText     string `json:"checkin_exp_text,omitempty"`
	CheckinStreakText  string `json:"checkin_streak_text,omitempty"`
}

type WeiboAppConfig struct {
	RawCapture     string `json:"raw_capture,omitempty"`
	Host           string `json:"host,omitempty"`
	RequestPath    string `json:"request_path,omitempty"`
	RequestBody    string `json:"request_body,omitempty"`
	CapturedOID    string `json:"captured_oid,omitempty"`
	Authorization  string `json:"authorization,omitempty"`
	GSID           string `json:"gsid,omitempty"`
	Aid            string `json:"aid,omitempty"`
	S              string `json:"s,omitempty"`
	XSessionID     string `json:"x_sessionid,omitempty"`
	XValidator     string `json:"x_validator,omitempty"`
	XShanhaiPass   string `json:"x_shanhai_pass,omitempty"`
	XLogUID        string `json:"x_log_uid,omitempty"`
	XEngineType    string `json:"x_engine_type,omitempty"`
	CronetRID      string `json:"cronet_rid,omitempty"`
	SNRT           string `json:"snrt,omitempty"`
	AcceptLanguage string `json:"accept_language,omitempty"`
	AcceptEncoding string `json:"accept_encoding,omitempty"`
	UserAgent      string `json:"user_agent,omitempty"`
}

type BilibiliConfig struct {
	RoomID string `json:"room_id"`
}

type WelcomeConfig struct {
	Enabled  bool     `json:"enabled"`
	Messages []string `json:"messages"`
}

type WeidianOrderConfig struct {
	Enabled      bool     `json:"enabled"`
	Cookie       string   `json:"cookie"`
	ShopID       string   `json:"shop_id"`
	BlockedItems []string `json:"blocked_items"`
	SpecialItems []string `json:"special_items"`
	AutoDelivery bool     `json:"auto_delivery"`
	PollInterval int      `json:"poll_interval"`
}

// LoadConfig loads the configuration from a file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.MessageGatewayURL) == "" {
		cfg.MessageGatewayURL = "http://127.0.0.1:8790"
	}
	if _, ok := raw["NIM_ROOM_MESSAGE_POLL_FALLBACK"]; !ok {
		cfg.NIMRoomMessagePollFallback = true
	}
	// QQ stays on unless it was explicitly disabled, so existing installs keep
	// working after the toggle was introduced.
	if _, ok := raw["QQ_ENABLED"]; !ok {
		cfg.QQEnabled = true
	}
	// First run after the platform-aware outlet landed: derive the address book
	// from existing subscriptions so operators see their current groups.
	if _, ok := raw["DELIVERY_TARGETS"]; !ok {
		cfg.SeedDeliveryTargets()
	}
	if cfg.FeishuGroupMap == nil {
		cfg.FeishuGroupMap = make(map[string]string)
	}
	if cfg.FeishuUploadConcurrency <= 0 {
		cfg.FeishuUploadConcurrency = 3
	}
	if _, ok := raw["NIM_LIVE_DANMAKU_ENABLED"]; !ok {
		cfg.NIMLiveDanmakuEnabled = true
	}
	if cfg.PocketMemberFeatures == nil {
		cfg.PocketMemberFeatures = make(map[string]*PocketMemberFeatureConfig)
	}
	if _, ok := raw["ANNUAL_SCORE_SPECIFIC"]; !ok || cfg.AnnualScoreSpecific == nil {
		cfg.AnnualScoreSpecific = make(map[string]bool)
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_ENABLED"]; !ok {
		cfg.WeiboSuperCountEnabled = false
	}
	if _, ok := raw["WEIBO_BROWSER_HEADLESS"]; !ok {
		cfg.WeiboBrowserHeadless = true
	}
	if strings.TrimSpace(cfg.WeiboBrowserAuthCmd) == "" {
		cfg.WeiboBrowserAuthCmd = "node ./sidecar/weibo-auth/index.mjs"
	}
	if strings.TrimSpace(cfg.WeiboBrowserProfileDir) == "" {
		cfg.WeiboBrowserProfileDir = "./storage/weibo-browser-profile"
	}
	if cfg.WeiboBrowserRefreshMinutes < 5 {
		cfg.WeiboBrowserRefreshMinutes = 30
	}
	if strings.TrimSpace(cfg.BrowserSidecarCmd) == "" {
		cfg.BrowserSidecarCmd = cfg.WeiboBrowserAuthCmd
	}
	if strings.TrimSpace(cfg.AlertEmailFrom) == "" {
		cfg.AlertEmailFrom = ""
	}
	if cfg.AlertEmailCooldownMinutes <= 0 {
		cfg.AlertEmailCooldownMinutes = 60
	}
	if strings.TrimSpace(cfg.BrowserProfileDir) == "" {
		cfg.BrowserProfileDir = cfg.WeiboBrowserProfileDir
	}
	// Prefer non-headless when unset so panel QR/Xvfb works; explicit config still wins.
	if _, ok := raw["BROWSER_HEADLESS"]; !ok {
		cfg.BrowserHeadless = false
	}
	if cfg.DouyinPollSeconds < 10 {
		cfg.DouyinPollSeconds = 60
	}
	if strings.TrimSpace(cfg.DouyinLiveWSURL) == "" {
		cfg.DouyinLiveWSURL = "ws://127.0.0.1:1088/ws"
	}
	if _, ok := raw["DOUYIN_LIVE_SUMMARY_ENABLED"]; !ok {
		cfg.DouyinLiveSummaryEnabled = false
	}
	if _, ok := raw["DOUYIN_LIVE_SOUND_WAVE_ENABLED"]; !ok {
		cfg.DouyinLiveSoundWaveEnabled = false
	}
	if cfg.DouyinSubscriptions == nil {
		cfg.DouyinSubscriptions = make(map[int64]map[string]*DouyinConfig)
	}
	if cfg.XiaohongshuPollSeconds < 30 {
		cfg.XiaohongshuPollSeconds = 60
	}
	if cfg.XiaohongshuSubscriptions == nil {
		cfg.XiaohongshuSubscriptions = make(map[int64]map[string]*XiaohongshuConfig)
	}
	if _, ok := raw["WEIBO_SUPERPOST_SUBSCRIPTIONS"]; !ok || cfg.WeiboSuperPostSubscriptions == nil {
		cfg.WeiboSuperPostSubscriptions = make(map[int64]map[string]*WeiboSuperPostConfig)
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_TOPICS"]; !ok || cfg.WeiboSuperCountTopics == nil {
		cfg.WeiboSuperCountTopics = make(map[string]*WeiboSuperCountTopic)
	}
	// Migration: if topics exist but no groups defined, create a default group
	if _, ok := raw["WEIBO_SUPER_COUNT_GROUPS"]; !ok || cfg.WeiboSuperCountGroups == nil {
		cfg.WeiboSuperCountGroups = make(map[string]*WeiboSuperCountGroupInfo)
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_IMAGE_GROUPS"]; !ok || cfg.WeiboSuperCountImageGroups == nil {
		cfg.WeiboSuperCountImageGroups = make(map[string]*WeiboSuperCountImageGroupInfo)
	}
	if len(cfg.WeiboSuperCountTopics) > 0 && len(cfg.WeiboSuperCountGroups) == 0 {
		hasGroup := false
		for _, t := range cfg.WeiboSuperCountTopics {
			if t.GroupName != "" {
				hasGroup = true
				break
			}
		}
		if !hasGroup {
			cfg.WeiboSuperCountGroups["default"] = &WeiboSuperCountGroupInfo{Name: "默认分组"}
			for _, t := range cfg.WeiboSuperCountTopics {
				t.GroupName = "default"
			}
		}
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_LAST_PUSH_DATE"]; !ok {
		cfg.WeiboSuperCountLastPushDate = ""
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS"]; !ok || cfg.WeiboSuperCountDailySnapshots == nil {
		cfg.WeiboSuperCountDailySnapshots = make(map[string]map[string]int)
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS_V2"]; !ok || cfg.WeiboSuperCountDailySnapshotsV2 == nil {
		cfg.WeiboSuperCountDailySnapshotsV2 = make(map[string]map[string]*WeiboSuperCountSnapshotItem)
	}
	if strings.TrimSpace(cfg.MediaDelivery) == "" {
		cfg.MediaDelivery = "local"
	} else {
		mode := strings.ToLower(strings.TrimSpace(cfg.MediaDelivery))
		if mode == "url" || mode == "direct" {
			mode = "remote"
		}
		if mode != "remote" {
			mode = "local"
		}
		cfg.MediaDelivery = mode
	}
	if cfg.AlertEmailSMTPPort < 0 {
		cfg.AlertEmailSMTPPort = 0
	}
	cfg.filePath = path
	// Migrate legacy GROUP_SUBSCRIPTIONS keys to canonical target ids once. The
	// rewrite is idempotent; persist immediately so a single write lands it.
	if cfg.MigrateGroupSubscriptionKeys() {
		_ = cfg.Save()
	}
	// 记录加载时的磁盘原值，供 Save() 判断本进程是否改过某个字段。
	cfg.baselineFields = raw
	return &cfg, nil
}

// Save saves the current configuration back to the file
func (c *Config) ConfigPath() string { return c.filePath }

// Save saves the current configuration back to the file.
//
// CRITICAL: 必须按键合并写回，不能直接 json.MarshalIndent(c) 整体覆盖。
// Config 是固定结构体，只声明了 Bot 自己关心的字段；而 config.json 里还有
// 大量由管理面板维护的键（BILIBILI_ENABLED / XIAOHONGSHU_ENABLED /
// DOUYIN_* 等），这些键不在结构体里。整体覆盖会在每次 Save 时把它们静默
// 删掉，表现为「面板开关莫名其妙又变回未启用」——历史上 B 站开关反复失灵
// 就是这个原因（面板保存投递目标、以及 Bot 侧 Save 都踩过）。
//
// 合并时以结构体为准：结构体里有的键取新值，结构体里没有的键原样保留。
func (c *Config) Save() error {
	encoded, err := json.MarshalIndent(c, "", "    ")
	if err != nil {
		return err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return err
	}

	merged := map[string]json.RawMessage{}
	if raw, readErr := os.ReadFile(c.filePath); readErr == nil {
		// 读失败时不阻断保存：结构体本身仍是完整可用的配置。
		_ = json.Unmarshal(raw, &merged)
	}
	// 基线感知合并（详见 baselineFields 注释）：
	//   - 内存值与加载时的基线相同 => 本进程没动过这个键，保留磁盘现值。
	//     这条修复了「长期驻留结构体把旧值写回」导致的配置回滚。
	//   - 内存值与基线不同        => 本进程确实改过，以内存值为准。
	for key, value := range fields {
		if base, ok := c.baselineFields[key]; ok && sameJSON(base, value) {
			continue
		}
		merged[key] = value
	}
	// 更新基线，使同一进程内后续 Save 的比较基准保持为「本进程最新状态」。
	c.baselineFields = fields

	data, err := json.MarshalIndent(merged, "", "    ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.filePath, data, 0600); err != nil {
		return err
	}
	return os.Chmod(c.filePath, 0600)
}

func (c *Config) UpdateToken(token string) {
	c.PocketToken = token
	_ = c.Save()
}

func (c *Config) UpdateNIMCredentials(account, token string) error {
	c.NIMAccount = strings.TrimSpace(account)
	c.NIMToken = strings.TrimSpace(token)
	return c.Save()
}

func (c *Config) IsAdmin(userID int64) bool {
	if userID == c.SuperAdmin {
		return true
	}
	for _, admin := range c.AdminQQ {
		if admin == userID {
			return true
		}
	}
	return false
}

func (c *Config) AddAdmin(userID int64) {
	for _, admin := range c.AdminQQ {
		if admin == userID {
			return
		}
	}
	c.AdminQQ = append(c.AdminQQ, userID)
	c.Save()
}

func (c *Config) RemoveAdmin(userID int64) {
	newAdmins := []int64{}
	for _, admin := range c.AdminQQ {
		if admin != userID {
			newAdmins = append(newAdmins, admin)
		}
	}
	c.AdminQQ = newAdmins
	c.Save()
}

// sameJSON 判断两段 JSON 是否语义一致（忽略空白与对象键序）。
// 必须支持顶层为数组/标量的情况：配置里的 DELIVERY_TARGETS、
// GROUP_SUBSCRIPTIONS 等都是数组或嵌套结构，不能只按对象解析。
func sameJSON(a, b json.RawMessage) bool {
	var va, vb interface{}
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	na, err1 := json.Marshal(va)
	nb, err2 := json.Marshal(vb)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(na) == string(nb)
}
