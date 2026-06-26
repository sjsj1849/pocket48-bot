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
	NIMAppDataDir                     string                                             `json:"NIM_APP_DATA_DIR"`
	NIMEntryMode                      string                                             `json:"NIM_ENTRY_MODE"`
	NIMAllowAnonFallback              bool                                               `json:"NIM_ALLOW_ANON_FALLBACK"`
	NIMRoomMessageEnabled             bool                                               `json:"NIM_ROOM_MESSAGE_ENABLED"`
	NIMRoomMessagePollFallback        bool                                               `json:"NIM_ROOM_MESSAGE_POLL_FALLBACK"`
	NIMViewerEventEnabled             bool                                               `json:"NIM_VIEWER_EVENT_ENABLED"`
	NIMIdolOnlineEventEnabled         bool                                               `json:"NIM_IDOL_ONLINE_EVENT_ENABLED"`
	NIMIdolOnlineNotifyEnabled        bool                                               `json:"NIM_IDOL_ONLINE_NOTIFY_ENABLED"`
	CrossRoomIdolSpeak                bool                                               `json:"CROSS_ROOM_IDOL_SPEAK"`
	PollingInterval                   int                                                `json:"POLLING_INTERVAL"`                            // Seconds
	LastStartupTime                   int64                                              `json:"LAST_STARTUP_TIME"`                           // Unix Timestamp
	WeiboSubscriptions                map[int64]map[string]*WeiboConfig                  `json:"WEIBO_SUBSCRIPTIONS"`                         // GroupID -> UID -> WeiboConfig
	WeiboSuperPostSubscriptions       map[int64]map[string]*WeiboSuperPostConfig         `json:"WEIBO_SUPERPOST_SUBSCRIPTIONS"`               // GroupID -> key(uid|oid) -> config
	WeiboSuperTopics                  map[int64]map[string]*WeiboSuperTopic              `json:"WEIBO_SUPER_TOPICS"`                          // GroupID -> OID -> Topic
	WeiboSuperAutoEnabled             bool                                               `json:"WEIBO_SUPER_AUTO_ENABLED"`                    // Daily auto super-topic sign-in
	WeiboSuperLastRunDate             string                                             `json:"WEIBO_SUPER_LAST_RUN_DATE"`                   // YYYY-MM-DD
	WeiboSuperCountEnabled            bool                                               `json:"WEIBO_SUPER_COUNT_ENABLED"`                   // Enable weibo super count feature
	WeiboSuperCountTopics             map[string]*WeiboSuperCountTopic                   `json:"WEIBO_SUPER_COUNT_TOPICS"`                    // OID -> Topic for count feature
	WeiboSuperCountLastPushDate       string                                             `json:"WEIBO_SUPER_COUNT_LAST_PUSH_DATE"`            // YYYY-MM-DD (Asia/Shanghai)
	WeiboSuperCountDailySnapshots     map[string]map[string]int                          `json:"WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS"`           // YYYY-MM-DD -> OID -> SignCount
	WeiboSuperCountDailySnapshotsV2   map[string]map[string]*WeiboSuperCountSnapshotItem `json:"WEIBO_SUPER_COUNT_DAILY_SNAPSHOTS_V2"`        // YYYY-MM-DD -> OID -> SnapshotItem
	WeiboAppAuthInvalidLastNotifyDate string                                             `json:"WEIBO_APP_AUTH_INVALID_LAST_NOTIFY_DATE,omitempty"`
	WeiboApp                          *WeiboAppConfig                                    `json:"WEIBO_APP,omitempty"`
	BilibiliSubscriptions             map[int64]map[string]*BilibiliConfig               `json:"BILIBILI_SUBSCRIPTIONS"`        // GroupID -> RoomID -> BilibiliConfig
	WeiboCookie                       string                                             `json:"WEIBO_COOKIE"`                  // Weibo web Cookie
	WeiboMWeiboCookie                 string                                             `json:"WEIBO_MWEIBO_COOKIE,omitempty"` // mweibo.com / m.weibo.cn Cookie
	DisableGroupCommands              bool                                               `json:"DISABLE_GROUP_COMMANDS"`        // Disable command handling in groups
	WelcomeConfigs                    map[int64]*WelcomeConfig                           `json:"WELCOME_CONFIGS"`               // GroupID -> WelcomeConfig
	WeidianOrders                     map[int64]*WeidianOrderConfig                      `json:"WEIDIAN_ORDERS"`                // GroupID -> WeidianOrderConfig
	filePath                          string
}

type WeiboConfig struct {
	UID    string `json:"uid"`
	AtAll  bool   `json:"at_all"`
	LastID string `json:"last_id,omitempty"`
}

type WeiboSuperTopic struct {
	OID            string `json:"oid"`
	Name           string `json:"name,omitempty"`
	LastSignDate   string `json:"last_sign_date,omitempty"`
	LastSignStatus string `json:"last_sign_status,omitempty"`
}

type WeiboSuperPostConfig struct {
	UID        string `json:"uid"`
	OID        string `json:"oid"`
	Name       string `json:"name,omitempty"`
	AtAll      bool   `json:"at_all"`
	LastPostID string `json:"last_post_id,omitempty"`
}

type WeiboSuperCountTopic struct {
	OID  string `json:"oid"`
	Name string `json:"name,omitempty"`
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
	if _, ok := raw["NIM_ALLOW_ANON_FALLBACK"]; !ok {
		cfg.NIMAllowAnonFallback = true
	}
	if _, ok := raw["NIM_ROOM_MESSAGE_POLL_FALLBACK"]; !ok {
		cfg.NIMRoomMessagePollFallback = true
	}
	if _, ok := raw["ANNUAL_SCORE_SPECIFIC"]; !ok || cfg.AnnualScoreSpecific == nil {
		cfg.AnnualScoreSpecific = make(map[string]bool)
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_ENABLED"]; !ok {
		cfg.WeiboSuperCountEnabled = false
	}
	if _, ok := raw["WEIBO_SUPERPOST_SUBSCRIPTIONS"]; !ok || cfg.WeiboSuperPostSubscriptions == nil {
		cfg.WeiboSuperPostSubscriptions = make(map[int64]map[string]*WeiboSuperPostConfig)
	}
	if _, ok := raw["WEIBO_SUPER_COUNT_TOPICS"]; !ok || cfg.WeiboSuperCountTopics == nil {
		cfg.WeiboSuperCountTopics = make(map[string]*WeiboSuperCountTopic)
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
	cfg.NormalizeNIMSettings()
	return &cfg, nil
}

func normalizeNIMEntryMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	switch m {
	case "", "auto":
		return "auto"
	case "im", "anon":
		return m
	default:
		return "auto"
	}
}

func (c *Config) NormalizeNIMSettings() {
	c.NIMEntryMode = normalizeNIMEntryMode(c.NIMEntryMode)
}

// Save saves the current configuration back to the file
func (c *Config) Save() error {
	c.NormalizeNIMSettings()
	data, err := json.MarshalIndent(c, "", "    ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.filePath, data, 0644)
}

func (c *Config) UpdateToken(token string) {
	c.PocketToken = token
	c.Save()
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
