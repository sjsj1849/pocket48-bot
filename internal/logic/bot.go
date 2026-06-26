package logic

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
	"pocket48-bot/internal/storage"
)

var qqFaceNameToID = map[string]string{
	"微笑":  "14",
	"撇嘴":  "1",
	"色":   "2",
	"发呆":  "3",
	"得意":  "4",
	"流泪":  "5",
	"害羞":  "6",
	"闭嘴":  "7",
	"睡":   "8",
	"大哭":  "9",
	"尴尬":  "10",
	"发怒":  "11",
	"调皮":  "12",
	"呲牙":  "13",
	"惊讶":  "15",
	"酷":   "16",
	"冷汗":  "96",
	"抓狂":  "18",
	"吐":   "19",
	"偷笑":  "20",
	"可爱":  "21",
	"白眼":  "22",
	"傲慢":  "23",
	"饥饿":  "24",
	"困":   "25",
	"惊恐":  "26",
	"流汗":  "27",
	"憨笑":  "28",
	"悠闲":  "29",
	"奋斗":  "30",
	"咒骂":  "31",
	"疑问":  "32",
	"嘘":   "33",
	"晕":   "34",
	"折磨":  "35",
	"衰":   "36",
	"骷髅":  "37",
	"敲打":  "38",
	"再见":  "39",
	"擦汗":  "97",
	"抠鼻":  "98",
	"鼓掌":  "99",
	"坏笑":  "100",
	"左哼哼": "101",
	"右哼哼": "102",
	"哈欠":  "103",
	"鄙视":  "104",
	"委屈":  "105",
	"快哭了": "106",
	"阴险":  "108",
	"亲亲":  "109",
	"可怜":  "111",
	"菜刀":  "112",
	"西瓜":  "113",
	"啤酒":  "114",
	"篮球":  "115",
	"乒乓":  "116",
	"咖啡":  "60",
	"饭":   "61",
	"猪头":  "46",
	"玫瑰":  "63",
	"凋谢":  "64",
	"嘴唇":  "67",
	"爱心":  "66",
	"心碎":  "65",
	"蛋糕":  "53",
	"闪电":  "54",
	"炸弹":  "55",
	"刀":   "56",
	"足球":  "57",
	"瓢虫":  "117",
	"便便":  "59",
	"月亮":  "75",
	"太阳":  "74",
	"礼物":  "69",
	"拥抱":  "49",
	"强":   "76",
	"弱":   "77",
	"握手":  "78",
	"胜利":  "79",
	"抱拳":  "118",
	"勾引":  "119",
	"拳头":  "120",
	"差劲":  "121",
	"爱你":  "122",
	"NO":  "123",
	"OK":  "124",
	"转圈":  "125",
	"磕头":  "126",
	"回头":  "127",
	"跳绳":  "128",
	"挥手":  "129",
	"激动":  "130",
	"街舞":  "131",
	"献吻":  "132",
	"左太极": "133",
	"右太极": "134",
}

var pocketMobilePattern = regexp.MustCompile(`^1\d{10}$`)

func (b *Bot) reply(event *napcat.Event, msg string) {
	if event.MessageType == "group" {
		b.napcat.SendGroupMessage(event.GroupID, napcat.TextSegment(msg))
	} else if event.MessageType == "private" {
		b.napcat.SendPrivateMessage(event.UserID, napcat.TextSegment(msg))
	}
}

func (b *Bot) notifyAdmins(msg string) {
	for _, uid := range b.collectAdminRecipients() {
		if uid == 0 {
			continue
		}
		b.napcat.SendPrivateMessage(uid, napcat.TextSegment(msg))
	}
}

func (b *Bot) collectAdminRecipients() []int64 {
	seen := make(map[int64]struct{})
	out := make([]int64, 0, 1+len(b.cfg.AdminQQ))
	if b.cfg.SuperAdmin != 0 {
		seen[b.cfg.SuperAdmin] = struct{}{}
		out = append(out, b.cfg.SuperAdmin)
	}
	for _, admin := range b.cfg.AdminQQ {
		if admin == 0 {
			continue
		}
		if _, ok := seen[admin]; ok {
			continue
		}
		seen[admin] = struct{}{}
		out = append(out, admin)
	}
	return out
}

type cachedUserDetail struct {
	info      *pocket48.UserDetailInfo
	expiresAt time.Time
}

type cachedRoomInfo struct {
	info      *pocket48.RoomInfo
	expiresAt time.Time
}

type GiftEventRecord struct {
	Timestamp      int64
	SpeakerUserID  int64
	SpeakerName    string
	GiftName       string
	GiftNum        int64
	ChickenLegUnit int64
	ChickenLegs    int64
	LegSource      string
}

type AnnualScoreGift struct {
	GiftID       int64
	GiftName     string
	GiftNum      int64
	ReceiverName string
	UnitScore    float64
	TotalScore   float64
}

type LiveGiftSession struct {
	LiveID        string
	LiveRoomID    int64
	LiveOwnerID   int64
	LiveOwnerName string
	StartedAt     int64
	Events        []GiftEventRecord
}

type Bot struct {
	cfg             *config.Config
	pocket          *pocket48.Client
	napcat          *napcat.Client
	weiboMonitor    *monitor.WeiboMonitor
	storage         *storage.Storage

	lastMsgTime            map[int64]int64
	cursorLoaded           map[int64]bool
	onMicState             map[int64]bool
	onMicLastCheck         map[int64]time.Time
	userDetailCache        map[int64]cachedUserDetail
	roomInfoCache          map[int64]cachedRoomInfo
	pendingPocketSMSMobile string
	pocketAuthExpired      bool
	mu                     sync.RWMutex

	isMonitoring       bool
	isLiveMonitoring   bool
	pollingInterval    time.Duration
	fastInterval       time.Duration // fast polling interval (~300ms) when messages detected
	pollFastMode       bool          // use fastInterval temporarily
	pollFastRemaining  int32         // remaining fast cycles
}

func NewBot(cfg *config.Config) *Bot {
	interval := time.Duration(cfg.PollingInterval) * time.Second
	if interval <= 0 {
		interval = 3 * time.Second
	}

	napcatClient := napcat.NewClient(cfg)
	weiboMon := monitor.NewWeiboMonitor(napcatClient)
	if cfg.WeiboCookie != "" {
		weiboMon.SetCookie(cfg.WeiboCookie)
	}
	if cfg.WeiboMWeiboCookie != "" {
		weiboMon.SetMWeiboCookie(cfg.WeiboMWeiboCookie)
	}
	if cfg.WeiboApp != nil {
		weiboMon.SetAppAuth(&monitor.WeiboAppAuth{
			RawCapture:     cfg.WeiboApp.RawCapture,
			Host:           cfg.WeiboApp.Host,
			RequestPath:    cfg.WeiboApp.RequestPath,
			RequestBody:    cfg.WeiboApp.RequestBody,
			CapturedOID:    cfg.WeiboApp.CapturedOID,
			Authorization:  cfg.WeiboApp.Authorization,
			GSID:           cfg.WeiboApp.GSID,
			Aid:            cfg.WeiboApp.Aid,
			S:              cfg.WeiboApp.S,
			XSessionID:     cfg.WeiboApp.XSessionID,
			XValidator:     cfg.WeiboApp.XValidator,
			XShanhaiPass:   cfg.WeiboApp.XShanhaiPass,
			XLogUID:        cfg.WeiboApp.XLogUID,
			XEngineType:    cfg.WeiboApp.XEngineType,
			CronetRID:      cfg.WeiboApp.CronetRID,
			SNRT:           cfg.WeiboApp.SNRT,
			AcceptLanguage: cfg.WeiboApp.AcceptLanguage,
			AcceptEncoding: cfg.WeiboApp.AcceptEncoding,
			UserAgent:      cfg.WeiboApp.UserAgent,
		})
	}

	// 如果 AppAuth 有 gsid 但 Cookie 为空，自动推导
	if cfg.WeiboCookie == "" && cfg.WeiboApp != nil && strings.TrimSpace(cfg.WeiboApp.GSID) != "" {
		cookieStr := "SUB=" + strings.TrimSpace(cfg.WeiboApp.GSID)
		cfg.WeiboCookie = cookieStr
		weiboMon.SetCookie(cookieStr)
	}

	// 初始化存储
	storageDir := "storage"
	cosDir := "/lhcos-data/bot48"
	botStorage := storage.NewStorage(storageDir, cosDir)

	if botStorage.IsCOSAvailable() {
		log.Println("✅ COS storage available, will archive messages")
	} else {
		log.Println("⚠️ COS not available, running in degraded mode")
	}

	bot := &Bot{
		cfg:                   cfg,
		pocket:                pocket48.NewClient(cfg),
		napcat:                napcatClient,
		weiboMonitor:          weiboMon,		storage:               botStorage,
		lastMsgTime:           make(map[int64]int64),
		cursorLoaded:          make(map[int64]bool),
		onMicState:            make(map[int64]bool),
		onMicLastCheck:        make(map[int64]time.Time),
		userDetailCache:       make(map[int64]cachedUserDetail),
		roomInfoCache:         make(map[int64]cachedRoomInfo),
		isMonitoring:          true,
		isLiveMonitoring:      cfg.LiveMonitoring,
		pollingInterval:       interval,
		fastInterval:          300 * time.Millisecond,
	}
	weiboMon.OnCookieInvalid = bot.notifyWeiboCookieInvalid

	// Set up welcome new member callback
	napcatClient.OnMemberJoin = bot.handleMemberJoin

	return bot
}

func (b *Bot) LogInfo(format string, v ...interface{}) {
	log.Printf("[INFO] "+format, v...)
}

func (b *Bot) getCachedUserDetail(userID int64) (*pocket48.UserDetailInfo, error) {
	if userID == 0 {
		return nil, nil
	}

	now := time.Now()
	b.mu.RLock()
	cached, ok := b.userDetailCache[userID]
	b.mu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.info, nil
	}

	detailInfo, err := b.pocket.GetUserDetailInfo(userID)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.userDetailCache[userID] = cachedUserDetail{info: detailInfo, expiresAt: now.Add(10 * time.Minute)}
	b.mu.Unlock()
	return detailInfo, nil
}

func (b *Bot) getCachedRoomInfo(roomID int64) (*pocket48.RoomInfo, error) {
	now := time.Now()
	b.mu.RLock()
	cached, ok := b.roomInfoCache[roomID]
	b.mu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.info, nil
	}

	info, err := b.pocket.GetRoomInfoByChannelID(roomID)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.roomInfoCache[roomID] = cachedRoomInfo{info: info, expiresAt: now.Add(5 * time.Minute)}
	b.mu.Unlock()
	return info, nil
}

func (b *Bot) Start() error {
	// Connect to NapCat
	if err := b.napcat.Connect(); err != nil {
		return fmt.Errorf("failed to connect to NapCat: %v", err)
	}

	// Register Event Handlers
	b.napcat.OnGroupMessage = b.handleGroupMessage
	b.napcat.OnPrivateMessage = b.handlePrivateMessage

	// Login to Pocket48
	b.LogInfo("Checking Pocket48 login credentials...")
	if b.cfg.PocketToken != "" {
		b.LogInfo("Token found. Verifying...")
		if err := b.pocket.CheckToken(); err == nil {
			b.LogInfo("Token valid. Using existing token for authentication.")
			b.LogInfo("Pocket48 (Token Mode) logged in successfully")
		} else {
			log.Printf("Token invalid or expired: %v", err)
			b.handlePocketAuthError(err)
		}
	} else {
		b.warnPocketLoginRequired("Pocket48 Token 未配置")
	}

	// Start Weibo monitor
	if b.migrateWeiboSuperPostSubscriptionsToBoundGroup() {
		if err := b.cfg.Save(); err != nil {
			log.Printf("Failed to migrate weibo superpost subscriptions to bound group: %v", err)
		} else {
			log.Printf("Migrated weibo superpost subscriptions from group 0 to bound group %d", b.cfg.BoundGroupID)
		}
	}

	if len(b.cfg.WeiboSubscriptions) > 0 || len(b.cfg.WeiboSuperPostSubscriptions) > 0 {
		b.LogInfo("Starting Weibo monitor...")
		for groupID, weiboConfigs := range b.cfg.WeiboSubscriptions {
			for uid, weiboConfig := range weiboConfigs {
				gid := groupID
				onNew := func(u, lastID string) {
					if b.cfg.WeiboSubscriptions[gid] != nil && b.cfg.WeiboSubscriptions[gid][u] != nil {
						b.cfg.WeiboSubscriptions[gid][u].LastID = lastID
						b.cfg.Save()
					}
				}

				if err := b.weiboMonitor.AddConfig(gid, uid, weiboConfig.AtAll, weiboConfig.LastID, onNew); err != nil {
					log.Printf("Failed to add weibo config for group %d, uid %s: %v", gid, uid, err)
				} else {
					b.LogInfo("Added weibo monitor for group %d, uid: %s", gid, uid)
				}
			}
		}
		for groupID, superPostConfigs := range b.cfg.WeiboSuperPostSubscriptions {
			for key, superPostConfig := range superPostConfigs {
				gid := groupID
				onNew := func(uid, oid, lastPostID string) {
					cfgs := b.cfg.WeiboSuperPostSubscriptions[gid]
					if cfgs != nil && cfgs[key] != nil {
						cfgs[key].LastPostID = lastPostID
						b.cfg.Save()
					}
				}
				if err := b.weiboMonitor.AddSuperPostConfig(gid, superPostConfig.UID, superPostConfig.OID, superPostConfig.Name, superPostConfig.AtAll, superPostConfig.LastPostID, onNew); err != nil {
					log.Printf("Failed to add weibo superpost config for group %d, key %s: %v", gid, key, err)
				} else {
					b.LogInfo("Added weibo superpost monitor for group %d, key: %s", gid, key)
				}
			}
		}
		b.weiboMonitor.Start()
	}
	go b.runWeiboSuperAutoSignLoop()
	go b.runWeiboSuperCountDailyPushLoop()

	// Start Polling Loop
	go b.pollLoop()

	// Start media cache cleanup
	go b.runMediaCleanupLoop()

	// Startup Notification
	startTime := time.Now()
	startTimeStr := startTime.Format("2006-01-02 15:04:05")
	lastTimeStr := "无 (首次启动)"
	if b.cfg.LastStartupTime > 0 {
		lastTimeStr = time.Unix(b.cfg.LastStartupTime, 0).Format("2006-01-02 15:04:05")
	}

	startupMsg := fmt.Sprintf("🤖 机器人已启动\n本次启动时间：%s\n上次启动时间：%s", startTimeStr, lastTimeStr)
	b.notifyAdmins(startupMsg)

	// Update LastStartupTime
	b.cfg.LastStartupTime = startTime.Unix()
	b.cfg.Save()

	// Graceful Shutdown Logic
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-stopChan
	b.LogInfo("Received signal: %v. Shutting down...", sig)

	// Runtime Calculation
	runTime := time.Since(startTime)
	hours := int(runTime.Hours())
	minutes := int(runTime.Minutes()) % 60
	seconds := int(runTime.Seconds()) % 60
	runTimeStr := fmt.Sprintf("%d小时%d分%d秒", hours, minutes, seconds)

	// Send Shutdown Notification
	shutdownMsg := fmt.Sprintf("⚠️ 机器人即将下线，服务暂时不可用。\n本次运行时间：%s", runTimeStr)
	b.notifyAdmins(shutdownMsg)
	time.Sleep(1 * time.Second)

	b.cfg.Save()

	return nil
}

func (b *Bot) handleGroupMessage(event *napcat.Event) {

	// Multi-group support: allow all groups

	// If group commands are disabled, don't respond to any group commands
	if b.cfg.DisableGroupCommands {
		return
	}

	// Check for at message and translate to command
	msg := strings.TrimSpace(event.RawMessage)

	// Check if message contains at bot
	if strings.Contains(msg, "[CQ:at,qq=") || strings.Contains(msg, "[CQ:at,all]") {
		cleanMsg := strings.ReplaceAll(msg, "[CQ:at,qq=3808515247]", "")
		cleanMsg = strings.ReplaceAll(cleanMsg, "[CQ:at,all]", "")
		cleanMsg = strings.TrimSpace(cleanMsg)
		if cleanMsg != "" {
			if b.tryHandleNaturalLanguage(event, cleanMsg) {
				return
			}
		}
	}

	// Check for standard command prefix
	prefix := b.cfg.CommandPrefix

	if strings.HasPrefix(msg, prefix) {
		args := parseCommandArgs(msg[len(prefix):])
		if len(args) > 0 {
			b.handleCommand(event, args)
		}
	}
}

func (b *Bot) warnPocketLoginRequired(reason string) {
	b.mu.Lock()
	if b.pocketAuthExpired {
		b.mu.Unlock()
		return
	}
	b.pocketAuthExpired = true
	b.mu.Unlock()

	log.Printf("[Pocket48] authorization unavailable: %s", reason)
	b.notifyAdmins(fmt.Sprintf("⚠️ Pocket48 授权已过期，房间消息轮询已暂停。\n原因: %s\n请手动登录：\nbot login sms <手机号>\n收到验证码后发送：bot code <验证码>", reason))
}

func (b *Bot) handlePocketAuthError(err error) bool {
	if !pocket48.IsAuthorizationExpired(err) {
		return false
	}
	b.warnPocketLoginRequired(err.Error())
	return true
}

func (b *Bot) clearPocketAuthExpired() {
	b.mu.Lock()
	b.pocketAuthExpired = false
	b.mu.Unlock()
}

func (b *Bot) isPocketAuthExpired() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pocketAuthExpired
}

func (b *Bot) tryHandleNaturalLanguage(event *napcat.Event, msg string) bool {

	msg = strings.ReplaceAll(msg, "[CQ:at,qq=3808515247]", "")
	msg = strings.ReplaceAll(msg, "[CQ:at,all]", "")
	msg = strings.TrimSpace(msg)
	lowerMsg := strings.ToLower(msg)

	if strings.HasPrefix(lowerMsg, "bot ") || strings.HasPrefix(lowerMsg, "weibo ") {
		return false
	}

	if msg == "帮助" || msg == "?" || msg == "help" {
		helpMsg := `📖 可用命令：
• 监控房间 <房间号> - 添加口袋房间
• 监控微博 <UID> - 添加微博监控
• 监控微博 <UID> 全体 - @全体
• 监控B站 <房间号> - 添加B站直播
• 查看监控 - 查看监控列表
• 删除微博监控 / 删除B站监控
• 开启监控 / 关闭监控
• 搜索 <名字> - 搜索房间
• 登录密码 <密码> - 密码登录
• 检查微博Cookie - 检查Cookie是否可用
• 重设微博Cookie <Cookie> - 热更新微博Cookie
• 直接粘贴抓包文本（含Set-Cookie）- 自动提取并更新
或直接发送命令: bot xxx`
		b.reply(event, helpMsg)
		return true
	}

	if strings.Contains(strings.ToLower(msg), "cookie") && strings.Contains(msg, "检查") {
		b.handleCommand(event, []string{"weibo", "cookie", "check"})
		return true
	}

	if strings.Contains(msg, "检查") && strings.Contains(msg, "微博") && strings.Contains(strings.ToLower(msg), "cookie") {
		b.handleCommand(event, []string{"weibo", "cookie", "check"})
		return true
	}

	if strings.Contains(msg, "重设") && strings.Contains(strings.ToLower(msg), "cookie") {
		cookie, ok := extractWeiboCookiePayload(msg)
		if !ok {
			b.reply(event, "格式错误: 重设微博Cookie <完整Cookie或SUB值>")
			return true
		}
		b.handleCommand(event, []string{"weibo", "cookie", "set", cookie})
		return true
	}

	if strings.Contains(msg, "更新") && strings.Contains(strings.ToLower(msg), "cookie") {
		cookie, ok := extractWeiboCookiePayload(msg)
		if !ok {
			b.reply(event, "格式错误: 更新微博Cookie <完整Cookie或SUB值>")
			return true
		}
		b.handleCommand(event, []string{"weibo", "cookie", "set", cookie})
		return true
	}

	if strings.Contains(lowerMsg, "api.weibo.cn") && (strings.Contains(lowerMsg, "authorization:") || strings.Contains(lowerMsg, "wb-sut")) {
		rawText := strings.TrimSpace(msg)
		if appCfg, ok := extractWeiboAppAuthFromCaptureText(rawText); ok {
			if err := b.updateWeiboAppAuth(appCfg); err != nil {
				b.reply(event, fmt.Sprintf("[错误] 导入微博 App 抓包失败: %v", err))
				return true
			}
			b.reply(event, fmt.Sprintf("[OK] 已导入微博 App 抓包参数: %s\n[OK] 后续超话统计/动态监控可优先走 App 通道", maskWeiboAppAuth(appCfg)))
			return true
		}
	}

	if strings.Contains(msg, "Set-Cookie:") && strings.Contains(msg, "SUB=") {
		cookie, ok := extractCookieFromCaptureText(msg)
		if !ok {
			b.reply(event, "[错误] 抓包文本里未提取到有效Cookie（至少需要SUB）")
			return true
		}
		b.handleCommand(event, []string{"weibo", "cookie", "set", cookie})
		return true
	}

	if strings.HasPrefix(msg, "搜索 ") {
		name := strings.TrimPrefix(msg, "搜索 ")
		name = strings.TrimSpace(name)
		if name != "" {
			b.handleCommand(event, []string{"search", name})
			return true
		}
	}

	if strings.Contains(msg, "监控") && strings.Contains(msg, "房间") {
		roomID, err := extractNumber(msg)
		if err == nil && roomID > 0 {
			b.handleCommand(event, []string{"monitor", strconv.FormatInt(roomID, 10)})
			return true
		}
	}

	if strings.Contains(msg, "查看") && strings.Contains(msg, "监控") {
		if strings.Contains(msg, "微博") {
			b.handleCommand(event, []string{"weibo", "list"})
			return true
		}
		b.handleCommand(event, []string{"list", "channels"})
		return true
	}

	if strings.Contains(msg, "监控") && strings.Contains(msg, "微博") {
		uid, err := extractNumber(msg)
		if err == nil && uid > 0 {
			atAll := strings.Contains(msg, "全体")
			atAllStr := ""
			if atAll {
				atAllStr = "at_all"
			}
			b.handleCommand(event, []string{"weibo", "add", strconv.FormatInt(uid, 10), atAllStr})
			return true
		}
	}

	if strings.Contains(msg, "删除") && strings.Contains(msg, "微博") && strings.Contains(msg, "监控") {
		b.handleCommand(event, []string{"weibo", "del"})
		return true
	}

	if (strings.Contains(msg, "开启") || strings.Contains(msg, "启动")) && strings.Contains(msg, "监控") {
		b.handleCommand(event, []string{"on"})
		return true
	}

	if (strings.Contains(msg, "关闭") || strings.Contains(msg, "停止")) && strings.Contains(msg, "监控") {
		b.handleCommand(event, []string{"off"})
		return true
	}

	// Password login: "登录密码 9624641314sj" or "密码登录"
	if (strings.Contains(msg, "登录") || strings.Contains(msg, "登陆")) && strings.Contains(msg, "密码") {
		// Extract password - try "登录密码 " prefix first
		password := strings.TrimPrefix(msg, "登录密码 ")
		password = strings.TrimPrefix(password, "登陆密码 ")
		password = strings.TrimPrefix(password, "密码登录 ")
		password = strings.TrimPrefix(password, "密码登陆 ")
		password = strings.TrimSpace(password)

		// Also try to extract from anywhere in the message
		if password == msg || password == "" {
			// Try to find password after 密码
			parts := strings.SplitN(msg, "密码", 2)
			if len(parts) > 1 {
				password = strings.TrimSpace(parts[1])
			}
		}

		if password == "" || password == "登录" || password == "登陆" || password == "密码" {
			b.reply(event, "请提供密码: 登录密码 <你的密码>")
			return true
		}

		// Clean password - remove any extra text
		var cleanPwd string
		for _, c := range password {
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
				cleanPwd += string(c)
			} else {
				break // Stop at first non-alphanumeric
			}
		}

		if cleanPwd != "" {
			b.handleCommand(event, []string{"login", "pwd", cleanPwd})
			return true
		}
	}

	return false
}

// encryptPlainPassword converts known plain passwords to encrypted form
// This is device-specific and hardcoded for known passwords

func (b *Bot) resolveTargetGroupID(event *napcat.Event) int64 {
	if event != nil && event.GroupID != 0 {
		return event.GroupID
	}
	if b.cfg.BoundGroupID != 0 {
		return b.cfg.BoundGroupID
	}
	return 0
}

func (b *Bot) handleArchiveCommand(args []string) string {
	if len(args) < 2 {
		return "用法: archive <status|retry>"
	}
	action := strings.ToLower(strings.TrimSpace(args[1]))
	switch action {
	case "status":
		cfg := b.storage.GetConfig()
		cos := "不可用"
		if b.storage.IsCOSAvailable() {
			cos = "可用"
		}
		return fmt.Sprintf("Archive状态\n- COS: %s\n- 目录: %s\n- 重试队列: %d\n- 切片阈值: %d 行 / %d 字节 / %d 秒刷新", cos, b.storage.GetArchiveDir(), b.storage.QueueLen(), cfg.MaxLines, cfg.MaxBytes, cfg.FlushInterval)
	case "retry":
		before := b.storage.QueueLen()
		if err := b.storage.RetryQueuedMessages(); err != nil {
			return fmt.Sprintf("重试失败: %v", err)
		}
		after := b.storage.QueueLen()
		return fmt.Sprintf("[OK] 已执行重试，队列 %d -> %d", before, after)
	}
	return "用法: archive <status|retry>"
}

func (b *Bot) HandleBotCommand(args []string) string {
	log.Printf("[BotCommand] 处理命令: %v", args)
	if len(args) == 0 {
		return ""
	}

	cmd := args[0]
	switch cmd {
	case "help":
		return `📖 可用命令：
• 搜索 <名字> - 搜索房间
• 监控 <房间号> - 添加监控
• 删除监控 <房间号> - 移除监控
• 查看监控 - 查看监控列表
• 开启监控 / 关闭监控
• 监控微博 <UID> - 添加微博监控
• 删除微博监控 - 移除微博监控
• 查看微博监控 - 查看微博监控
• 年度青春盛典记分 - score <on/off> <房间ID>
• 检查微博Cookie - 检查Cookie状态
• 状态 - 查看转发状态
• 注册 - 开启消息转发
• 取消 - 关闭消息转发`

	case "list", "channels":
		groupIDStr := strconv.FormatInt(b.cfg.BoundGroupID, 10)
		rooms := b.cfg.GroupSubscriptions[groupIDStr]

		if len(rooms) == 0 {
			return "📊 当前没有正在监控的频道。"
		}

		b.LogInfo("正在获取频道详情...")
		var sb strings.Builder
		sb.WriteString("📊 当前监控的频道:\n")

		for _, roomID := range rooms {
			info, err := b.getCachedRoomInfo(roomID)
			if err != nil {
				sb.WriteString(fmt.Sprintf("- ID: %d (获取详情失败)\n", roomID))
			} else {
				sb.WriteString(fmt.Sprintf("- ID: %d | 频道: %s | 主播: %s\n", roomID, info.ChannelName, info.OwnerName))
			}
		}
		return sb.String()

	case "search":
		if len(args) < 2 {
			return "请提供搜索关键词: 搜索 <名字>"
		}
		query := args[1]
		servers, err := b.pocket.Search(query)
		if err != nil {
			return fmt.Sprintf("搜索失败: %v", err)
		}
		if len(servers) == 0 {
			return "未找到结果: " + query
		}
		var sb strings.Builder
		sb.WriteString("🔍 搜索结果:\n")
		for _, server := range servers {
			ids, _ := b.pocket.GetChannelIDByServerID(server.ServerID)
			for _, id := range ids {
				if id >= 0 {
					roomIdStr := fmt.Sprintf("(房间ID: %d)", id)
					channelInfo, err := b.getCachedRoomInfo(id)
					if err != nil {
						sb.WriteString(fmt.Sprintf("- %s %s\n", "获取详情失败", roomIdStr))
					} else {
						sb.WriteString(fmt.Sprintf("- %s %s\n", channelInfo.ChannelName, roomIdStr))
					}
				}
			}
		}
		return sb.String()

	case "monitor":
		if len(args) < 2 {
			return "请提供房间号: 监控 <房间号>"
		}
		roomID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		groupIDStr := strconv.FormatInt(b.cfg.BoundGroupID, 10)
		if b.cfg.GroupSubscriptions == nil {
			b.cfg.GroupSubscriptions = make(map[string][]int64)
		}
		for _, id := range b.cfg.GroupSubscriptions[groupIDStr] {
			if id == roomID {
				return fmt.Sprintf("房间 %d 已经在监控列表中", roomID)
			}
		}
		b.cfg.GroupSubscriptions[groupIDStr] = append(b.cfg.GroupSubscriptions[groupIDStr], roomID)
		b.cfg.Save()
		return fmt.Sprintf("[OK] 已添加房间 %d 到监控列表", roomID)

	case "remove":
		if len(args) < 2 {
			return "请提供房间号: 删除监控 <房间号>"
		}
		roomID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		groupIDStr := strconv.FormatInt(b.cfg.BoundGroupID, 10)
		currentRooms := b.cfg.GroupSubscriptions[groupIDStr]
		newRooms := []int64{}
		found := false
		for _, id := range currentRooms {
			if id == roomID {
				found = true
				continue
			}
			newRooms = append(newRooms, id)
		}
		if found {
			b.cfg.GroupSubscriptions[groupIDStr] = newRooms
			b.cfg.Save()
			return fmt.Sprintf("[OK] 已移除房间 %d 的监控", roomID)
		}
		return fmt.Sprintf("房间 %d 不在监控列表中", roomID)

	case "on":
		b.isMonitoring = true
		return "✅ 监控已开启"

	case "off":
		b.isMonitoring = false
		return "✅ 监控已关闭"

	case "live":
		if len(args) < 2 {
			state := "开启"
			if !b.cfg.LiveMonitoring {
				state = "关闭"
			}
			return fmt.Sprintf("全局直播监控状态: %s", state)
		}
		action := strings.ToLower(args[1])
		if action == "on" {
			b.cfg.LiveMonitoring = true
			b.cfg.Save()
			return "[OK] 全局直播监控已开启"
		} else if action == "off" {
			b.cfg.LiveMonitoring = false
			b.cfg.Save()
			return "[OK] 全局直播监控已关闭"
		}
		return "格式错误: live <on/off>"

	case "gift":
		if len(args) < 3 {
			return "格式错误: gift <on/off> <房间号>"
		}
		action := strings.ToLower(args[1])
		roomID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		roomIDStr := strconv.FormatInt(roomID, 10)
		if b.cfg.GiftSpecific == nil {
			b.cfg.GiftSpecific = make(map[string]bool)
		}
		if action == "on" {
			b.cfg.GiftSpecific[roomIDStr] = true
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的礼物回复已开启", roomID)
		} else if action == "off" {
			b.cfg.GiftSpecific[roomIDStr] = false
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的礼物回复已关闭", roomID)
		}
		return "格式错误: gift <on/off> <房间号>"

	case "score":
		if len(args) < 3 {
			return "格式错误: score <on/off> <房间号>"
		}
		action := strings.ToLower(args[1])
		roomID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		roomIDStr := strconv.FormatInt(roomID, 10)
		if b.cfg.AnnualScoreSpecific == nil {
			b.cfg.AnnualScoreSpecific = make(map[string]bool)
		}
		if action == "on" {
			b.cfg.AnnualScoreSpecific[roomIDStr] = true
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的年度青春盛典记分监控已开启", roomID)
		} else if action == "off" {
			b.cfg.AnnualScoreSpecific[roomIDStr] = false
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的年度青春盛典记分监控已关闭", roomID)
		}
		return "格式错误: score <on/off> <房间号>"

	case "weibo":
		if len(args) < 2 {
			return "格式错误: weibo <add/del/list|cookie|super> [参数]"
		}
		action := strings.ToLower(strings.TrimSpace(args[1]))

		if action == "super" {
			evt := &napcat.Event{GroupID: b.cfg.BoundGroupID}
			return b.handleWeiboSuperCommand(evt, args)
		}

		if action == "cookie" {
			if len(args) < 3 {
				return "用法: weibo cookie <check|set|reset|import> [Cookie]"
			}
			subCmd := strings.ToLower(strings.TrimSpace(args[2]))
			switch subCmd {
			case "check":
				ok, detail, err := b.checkWeiboCookieStatus()
				if err != nil {
					return fmt.Sprintf("[错误] Cookie检查失败: %v", err)
				}
				if ok {
					return fmt.Sprintf("[OK] 微博 Cookie 可用: %s", detail)
				}
				return fmt.Sprintf("[警告] 微博 Cookie 异常: %s\n请执行: weibo cookie set <Cookie>", detail)
			case "import":
				if len(args) < 4 {
					return "格式错误: weibo cookie import <抓包文本>"
				}
				rawText := strings.TrimSpace(strings.Join(args[3:], " "))
				if appCfg, ok := extractWeiboAppAuthFromCaptureText(rawText); ok {
					if err := b.updateWeiboAppAuth(appCfg); err != nil {
						return fmt.Sprintf("[错误] 导入微博 App 抓包失败: %v", err)
					}
					masked := maskWeiboAppAuth(appCfg)
					return fmt.Sprintf("[OK] 已导入微博 App 抓包参数: %s\n[OK] 后续超话统计/动态监控可优先走 App 通道", masked)
				}
				parsedCookie, ok := extractCookieFromCaptureText(rawText)
				if !ok {
					return "[错误] 未从抓包文本中提取到有效Cookie（至少需要SUB），且未识别到有效 App 抓包"
				}
				masked, err := b.updateWeiboCookie(0, parsedCookie)
				if err != nil {
					return fmt.Sprintf("[错误] 导入微博 Cookie 失败: %v", err)
				}
				ok2, detail, checkErr := b.checkWeiboCookieStatus()
				if checkErr != nil {
					return fmt.Sprintf("[OK] 已从抓包文本导入Cookie: %s\n[提示] 更新后检查失败: %v", masked, checkErr)
				}
				if ok2 {
					return fmt.Sprintf("[OK] 已从抓包文本导入Cookie: %s\n[OK] 可用性检查: %s", masked, detail)
				}
				return fmt.Sprintf("[OK] 已从抓包文本导入Cookie: %s\n[警告] 可用性检查异常: %s", masked, detail)
			case "set", "reset":
				if len(args) < 4 {
					return "格式错误: weibo cookie set <完整Cookie或SUB值>"
				}
				cookie := strings.TrimSpace(strings.Join(args[3:], " "))
				masked, err := b.updateWeiboCookie(0, cookie)
				if err != nil {
					return fmt.Sprintf("[错误] 更新微博 Cookie 失败: %v", err)
				}
				ok, detail, checkErr := b.checkWeiboCookieStatus()
				if checkErr != nil {
					return fmt.Sprintf("[OK] 微博 Cookie 已更新: %s\n[提示] 检查失败: %v", masked, checkErr)
				}
				if ok {
					return fmt.Sprintf("[OK] 微博 Cookie 已更新: %s\n[OK] 可用: %s", masked, detail)
				}
				return fmt.Sprintf("[OK] 微博 Cookie 已更新: %s\n[警告] 异常: %s", masked, detail)
			}
			return "用法: weibo cookie <check|set|reset|import> [Cookie]"
		}

		if action == "list" {
			if b.cfg.WeiboSubscriptions == nil || len(b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]) == 0 {
				return "该群暂无微博监控"
			}
			var uids []string
			for uid := range b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID] {
				uids = append(uids, uid)
			}
			return fmt.Sprintf("📊 微博监控: UID=%s", strings.Join(uids, ", "))
		}

		if action == "add" {
			if len(args) < 3 {
				return "格式错误: weibo add <UID> [at_all]"
			}
			uid := args[2]
			atAll := len(args) >= 4 && args[3] == "at_all"

			if b.cfg.WeiboSubscriptions == nil {
				b.cfg.WeiboSubscriptions = make(map[int64]map[string]*config.WeiboConfig)
			}
			if _, ok := b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]; !ok {
				b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID] = make(map[string]*config.WeiboConfig)
			}
			b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID][uid] = &config.WeiboConfig{
				UID:    uid,
				AtAll:  atAll,
				LastID: "",
			}
			b.cfg.Save()

			onNew := func(u, newLastID string) {
				if b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID] != nil && b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID][u] != nil {
					b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID][u].LastID = newLastID
					b.cfg.Save()
				}
			}
			b.weiboMonitor.AddConfig(b.cfg.BoundGroupID, uid, atAll, "", onNew)
			b.weiboMonitor.Start()
			return fmt.Sprintf("[OK] 添加微博监控: UID=%s, @全体=%v", uid, atAll)
		}

		if action == "del" {
			if len(args) < 3 {
				delete(b.cfg.WeiboSubscriptions, b.cfg.BoundGroupID)
				b.cfg.Save()
				b.weiboMonitor.RemoveConfig(b.cfg.BoundGroupID, "")
				return "[OK] 已删除该群的所有微博监控"
			}
			uidToDel := args[2]
			if groupSubs, ok := b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]; ok {
				if _, ok := groupSubs[uidToDel]; ok {
					delete(groupSubs, uidToDel)
					if len(groupSubs) == 0 {
						delete(b.cfg.WeiboSubscriptions, b.cfg.BoundGroupID)
					}
					b.cfg.Save()
					b.weiboMonitor.RemoveConfig(b.cfg.BoundGroupID, uidToDel)
					return fmt.Sprintf("[OK] 已删除微博监控 UID: %s", uidToDel)
				}
			}
			return "未找到要删除的 UID"
		}

		return "格式错误: weibo <add/del/list|cookie|super> [参数]"

	case "archive":
		return b.handleArchiveCommand(args)

	case "status":
		return "📊 Bot 运行中"

	default:
		return "❓ 未知命令，发送「帮助」查看可用命令"
	}
}
