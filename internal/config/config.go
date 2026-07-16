package config

import (
	"encoding/json"
	"os"
	"strings"
)

type Config struct {
	NapCatWSURL                       string                                             `json:"NAPCAT_WS_URL"`
	NapCatAccessToken                 string                                             `json:"NAPCAT_ACCESS_TOKEN"`
	PocketUsername                    string                                             `json:"POCKET_USERNAME"`
	PocketPassword                    string                                             `json:"POCKET_PASSWORD"`
	PocketToken                       string                                             `json:"POCKET_TOKEN"`
	NIMToken                          string                                             `json:"NIM_TOKEN"`
	AdminQQ                           []int64                                            `json:"ADMIN_QQ"` // Changed to array of int64
	SuperAdmin                        int64                                              `json:"SUPER_ADMIN"`
	BoundGroupID                      int64                                              `json:"BOUND_GROUP_ID"`
	CommandPrefix                     string                                             `json:"COMMAND_PREFIX"`
	GroupSubscriptions                map[string][]int64                                 `json:"GROUP_SUBSCRIPTIONS"`  // GroupID (string) -> List of RoomIDs
	InitialFetchWindow                int64                                              `json:"INITIAL_FETCH_WINDOW"` // Minutes
	LiveMonitoring                    bool                                               `json:"LIVE_MONITORING"`
	LiveSpecific                      map[string]bool                                    `json:"LIVE_SPECIFIC"`         // RoomID (string) -> bool
	GiftSpecific                      map[string]bool                                    `json:"GIFT_SPECIFIC"`         // RoomID (string) -> bool
	AnnualScoreSpecific               map[string]bool                                    `json:"ANNUAL_SCORE_SPECIFIC"` // RoomID (string) -> bool
	NIMEnabled                        bool                                               `json:"NIM_ENABLED"`
	NIMSidecarCmd                     string                                             `json:"NIM_SIDECAR_CMD"`
	NIMAccount                        string                                             `json:"NIM_ACCOUNT"`
	NIMRoomMessageEnabled             bool                                               `json:"NIM_ROOM_MESSAGE_ENABLED"`
	NIMRoomMessagePollFallback        bool                                               `json:"NIM_ROOM_MESSAGE_POLL_FALLBACK"`
	NIMLiveDanmakuEnabled             bool                                               `json:"NIM_LIVE_DANMAKU_ENABLED"`
	NIMViewerEventEnabled             bool                                               `json:"NIM_VIEWER_EVENT_ENABLED"`
	PollingInterval                   int                                                `json:"POLLING_INTERVAL"`                     // Seconds
	LastStartupTime                   int64                                              `json:"LAST_STARTUP_TIME"`                    // Unix Timestamp
	WeiboSubscriptions                map[int64]map[string]*WeiboConfig                  `json:"WEIBO_SUBSCRIPTIONS"`                  // GroupID -> UID -> WeiboConfig
	WeiboSuperPostSubscriptions       map[int64]map[string]*WeiboSuperPostConfig         `json:"WEIBO_SUPERPOST_SUBSCRIPTIONS"`        // GroupID -> key(uid|oid) -> config
	WeiboSuperTopics                  map[int64]map[string]*WeiboSuperTopic              `json:"WEIBO_SUPER_TOPICS"`                   // GroupID -> OID -> Topic
	WeiboSuperAutoEnabled             bool                                               `json:"WEIBO_SUPER_AUTO_ENABLED"`             // Daily auto super-topic sign-in
	WeiboSuperLastRunDate             string                                             `json:"WEIBO_SUPER_LAST_RUN_DATE"`            // YYYY-MM-DD
	WeiboSuperCountEnabled            bool                                               `json:"WEIBO_SUPER_COUNT_ENABLED"`            // Enable weibo super count feature
	WeiboSuperCountTopics             map[string]*WeiboSuperCountTopic                   `json:"WEIBO_SUPER_COUNT_TOPICS"`             // OID -> Topic for count feature
	WeiboSuperCountGroups             map[string]*WeiboSuperCountGroupInfo               `json:"WEIBO_SUPER_COUNT_GROUPS"`             // group_id -> group info
	WeiboSuperCountLastPushDate       string                                             `json:"WEIBO_SUPER_COUNT_LAST_PUSH_DATE"`     // YYYY-MM-DD (Asia/Shanghai)
	WeiboSuperCountDailySnapshots     map[string]map[string]int                          `json:"WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS"`    // YYYY-MM-DD -> OID -> SignCount
	WeiboSuperCountDailySnapshotsV2   map[string]map[string]*WeiboSuperCountSnapshotItem `json:"WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS_V2"` // YYYY-MM-DD -> OID -> SnapshotItem
	WeiboAppAuthInvalidLastNotifyDate string                                             `json:"WEIBO_APP_AUTH_INVALID_LAST_NOTIFY_DATE,omitempty"`
	WeiboAppAuthHealthCheckNotifyAt   string                                             `json:"WEIBO_APP_AUTH_HEALTH_CHECK_NOTIFY_AT,omitempty"` // 主动健康检查上次通知时间 (unix ts)
	WeiboApp                          *WeiboAppConfig                                    `json:"WEIBO_APP,omitempty"`
	BilibiliSubscriptions             map[int64]map[string]*BilibiliConfig               `json:"BILIBILI_SUBSCRIPTIONS"`        // GroupID -> RoomID -> BilibiliConfig
	WeiboCookie                       string                                             `json:"WEIBO_COOKIE"`                  // Weibo web Cookie
	WeiboMWeiboCookie                 string                                             `json:"WEIBO_MWEIBO_COOKIE,omitempty"` // mweibo.com / m.weibo.cn Cookie
	WeiboBrowserAuthEnabled           bool                                               `json:"WEIBO_BROWSER_AUTH_ENABLED"`
	WeiboBrowserAuthCmd               string                                             `json:"WEIBO_BROWSER_AUTH_CMD"`
	WeiboBrowserProfileDir            string                                             `json:"WEIBO_BROWSER_PROFILE_DIR"`
	WeiboBrowserHeadless              bool                                               `json:"WEIBO_BROWSER_HEADLESS"`
	WeiboBrowserRefreshMinutes        int                                                `json:"WEIBO_BROWSER_REFRESH_MINUTES"`
	BrowserSidecarCmd                 string                                             `json:"BROWSER_SIDECAR_CMD"`
	BrowserProfileDir                 string                                             `json:"BROWSER_PROFILE_DIR"`
	BrowserHeadless                   bool                                               `json:"BROWSER_HEADLESS"`
	DouyinEnabled                     bool                                               `json:"DOUYIN_ENABLED"`
	DouyinPollSeconds                 int                                                `json:"DOUYIN_POLL_SECONDS"`
	DouyinLiveWSURL                   string                                             `json:"DOUYIN_LIVE_WS_URL"`
	DouyinLiveSidecarCmd              string                                             `json:"DOUYIN_LIVE_SIDECAR_CMD"`
	DouyinSubscriptions               map[int64]map[string]*DouyinConfig                 `json:"DOUYIN_SUBSCRIPTIONS"`
	DouyinSpecialFollowEnabled        bool                                               `json:"DOUYIN_SPECIAL_FOLLOW_ENABLED"`
	DouyinSpecialFollowMinutes        int                                                `json:"DOUYIN_SPECIAL_FOLLOW_MINUTES"`
	DouyinSpecialFollowIDs            []string                                           `json:"DOUYIN_SPECIAL_FOLLOW_IDS"`
	DouyinIMEnabled                   bool                                               `json:"DOUYIN_IM_ENABLED"`
	DouyinIMPrivateEnabled            bool                                               `json:"DOUYIN_IM_PRIVATE_ENABLED"`
	DouyinIMGroupName                 string                                             `json:"DOUYIN_IM_GROUP_NAME"`
	DouyinIMGroupNumber               string                                             `json:"DOUYIN_IM_GROUP_NUMBER"`
	DisableGroupCommands              bool                                               `json:"DISABLE_GROUP_COMMANDS"` // Disable command handling in groups
	WelcomeConfigs                    map[int64]*WelcomeConfig                           `json:"WELCOME_CONFIGS"`        // GroupID -> WelcomeConfig
	WeidianOrders                     map[int64]*WeidianOrderConfig                      `json:"WEIDIAN_ORDERS"`         // GroupID -> WeidianOrderConfig
	filePath                          string
}

type WeiboConfig struct {
	UID    string `json:"uid"`
	AtAll  bool   `json:"at_all"`
	LastID string `json:"last_id,omitempty"`
}

// DouyinConfig stores one creator subscription. SecUserID is the stable key
// used by creator pages; LiveID is the live.douyin.com path discovered from
// the creator page and can be reused while the creator is offline.
type DouyinConfig struct {
	SecUserID   string `json:"sec_user_id"`
	ProfileURL  string `json:"profile_url,omitempty"`
	Name        string `json:"name,omitempty"`
	AtAll       bool   `json:"at_all"`
	LastAwemeID string `json:"last_aweme_id,omitempty"`
	LiveID      string `json:"live_id,omitempty"`
	Auto        bool   `json:"auto,omitempty"`
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
	Heat24h            string `json:"heat24h,omitempty"`
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
	if _, ok := raw["NIM_ROOM_MESSAGE_POLL_FALLBACK"]; !ok {
		cfg.NIMRoomMessagePollFallback = true
	}
	if _, ok := raw["NIM_LIVE_DANMAKU_ENABLED"]; !ok {
		cfg.NIMLiveDanmakuEnabled = true
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
	if strings.TrimSpace(cfg.BrowserProfileDir) == "" {
		cfg.BrowserProfileDir = cfg.WeiboBrowserProfileDir
	}
	if _, ok := raw["BROWSER_HEADLESS"]; !ok {
		cfg.BrowserHeadless = cfg.WeiboBrowserHeadless
	}
	if cfg.DouyinPollSeconds < 15 {
		cfg.DouyinPollSeconds = 60
	}
	if strings.TrimSpace(cfg.DouyinLiveWSURL) == "" {
		cfg.DouyinLiveWSURL = "ws://127.0.0.1:1088/ws"
	}
	if cfg.DouyinSubscriptions == nil {
		cfg.DouyinSubscriptions = make(map[int64]map[string]*DouyinConfig)
	}
	if cfg.DouyinSpecialFollowMinutes < 10 {
		cfg.DouyinSpecialFollowMinutes = 30
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
	cfg.filePath = path
	return &cfg, nil
}

// Save saves the current configuration back to the file
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "    ")
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
