package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type configKind string

const (
	kindString     configKind = "string"
	kindInteger    configKind = "integer"
	kindBoolean    configKind = "boolean"
	kindSecret     configKind = "secret"
	kindStringList configKind = "stringList"
)

type configField struct {
	Key             string     `json:"key"`
	Group           string     `json:"group"`
	Label           string     `json:"label"`
	Description     string     `json:"description"`
	Kind            configKind `json:"kind"`
	Configured      bool       `json:"configured,omitempty"` // for secrets
	RestartRequired bool       `json:"restartRequired,omitempty"`
	Value           any        `json:"value"`
}

var configFields = map[string]configField{
	// Bot
	"NAPCAT_WS_URL":                {"NAPCAT_WS_URL", "Bot", "NapCat WebSocket 地址", "例如 ws://localhost:3001", kindString, false, false, nil},
	"NAPCAT_ACCESS_TOKEN":          {"NAPCAT_ACCESS_TOKEN", "Bot", "NapCat 访问令牌", "留空如果不需要", kindSecret, false, false, nil},
	"BOUND_GROUP_ID":               {"BOUND_GROUP_ID", "Bot", "默认通知群号", "Bot 内部通知和未特别指定群号的订阅转发到该群", kindInteger, false, false, nil},
	"COMMAND_PREFIX":               {"COMMAND_PREFIX", "Bot", "命令前缀", "触发的命令符号", kindString, false, false, nil},
	"DISABLE_GROUP_COMMANDS":       {"DISABLE_GROUP_COMMANDS", "Bot", "禁用群聊指令", "开启后不再响应群里的手动命令", kindBoolean, false, false, nil},
	"MEDIA_DELIVERY":               {"MEDIA_DELIVERY", "Bot", "媒体发送方式", "local（本机下载转发）或 remote（直传链接）", kindString, false, false, nil},
	"ALERT_EMAIL_ENABLED":          {"ALERT_EMAIL_ENABLED", "Bot", "启用邮件告警", "当发生需要人工处理的错误时发送", kindBoolean, false, false, nil},
	"ALERT_EMAIL_TO":               {"ALERT_EMAIL_TO", "Bot", "告警收件人", "多个用逗号分隔", kindString, false, false, nil},
	"ALERT_EMAIL_FROM":             {"ALERT_EMAIL_FROM", "Bot", "发件人地址", "SMTP 发件箱", kindString, false, false, nil},
	"ALERT_EMAIL_SMTP_HOST":        {"ALERT_EMAIL_SMTP_HOST", "Bot", "SMTP 服务器", "例如 smtp.gmail.com", kindString, false, false, nil},
	"ALERT_EMAIL_SMTP_PORT":        {"ALERT_EMAIL_SMTP_PORT", "Bot", "SMTP 端口", "常见 587（TLS）或 465（SSL）", kindInteger, false, false, nil},
	"ALERT_EMAIL_SMTP_USER":        {"ALERT_EMAIL_SMTP_USER", "Bot", "SMTP 用户名", "邮箱完整地址", kindString, false, false, nil},
	"ALERT_EMAIL_SMTP_PASSWORD":    {"ALERT_EMAIL_SMTP_PASSWORD", "Bot", "SMTP 密码", "邮箱密码或应用专用密码", kindSecret, false, false, nil},
	"ALERT_EMAIL_COOLDOWN_MINUTES": {"ALERT_EMAIL_COOLDOWN_MINUTES", "Bot", "告警冷却时间（分钟）", "同类型告警至少间隔这么久", kindInteger, false, false, nil},
	"ADMIN_PANEL_URL":              {"ADMIN_PANEL_URL", "Bot", "面板地址（可选）", "日报等链接会用到", kindString, false, false, nil},
	// 口袋48
	"POCKET_USERNAME":                {"POCKET_USERNAME", "口袋48", "口袋48 手机号", "登录用手机号", kindString, false, false, nil},
	"POCKET_PASSWORD":                {"POCKET_PASSWORD", "口袋48", "口袋48 密码", "登录用密码", kindSecret, false, false, nil},
	"POCKET_TOKEN":                   {"POCKET_TOKEN", "口袋48", "Token（自动）", "登录成功后自动获取，留空让系统自动登录", kindSecret, false, false, nil},
	"LIVE_MONITORING":                {"LIVE_MONITORING", "口袋48", "直播监控", "监控口袋48直播状态", kindBoolean, false, false, nil},
	"POLLING_INTERVAL":               {"POLLING_INTERVAL", "口袋48", "消息轮询间隔（秒）", "口袋48实时消息轮询间隔，建议 3-5 秒", kindInteger, false, false, nil},
	"NIM_ENABLED":                    {"NIM_ENABLED", "口袋48", "NIM 实时消息（IM）", "通过 NIM SDK 获取实时消息，比轮询快", kindBoolean, false, false, nil},
	"NIM_ROOM_MESSAGE_ENABLED":       {"NIM_ROOM_MESSAGE_ENABLED", "口袋48", "NIM 房间消息", "将房间实时消息转发到 QQ", kindBoolean, false, false, nil},
	"NIM_ROOM_MESSAGE_POLL_FALLBACK": {"NIM_ROOM_MESSAGE_POLL_FALLBACK", "口袋48", "NIM 轮询兜底", "NIM 连接异常时自动切换至轮询模式", kindBoolean, false, false, nil},
	"NIM_LIVE_DANMAKU_ENABLED":       {"NIM_LIVE_DANMAKU_ENABLED", "口袋48", "NIM 直播弹幕", "转发口袋48直播间弹幕", kindBoolean, false, false, nil},
	"NIM_VIEWER_EVENT_ENABLED":       {"NIM_VIEWER_EVENT_ENABLED", "口袋48", "成员串门提醒", "仅转发已识别成员进入/离开其他成员直播间，并在可计算时附观看时长；普通粉丝会被过滤", kindBoolean, false, false, nil},
	// 微博
	"WEIBO_BROWSER_AUTH_ENABLED":    {"WEIBO_BROWSER_AUTH_ENABLED", "微博", "启用浏览器侧卡登录", "开启后用面板「浏览器」页扫码，自动维护微博/抖音/小红书登录态（Cookie）。推荐；比手贴 Cookie 稳", kindBoolean, false, false, nil},
	"WEIBO_BROWSER_REFRESH_MINUTES": {"WEIBO_BROWSER_REFRESH_MINUTES", "微博", "Cookie 刷新周期", "自动刷新间隔（分钟，最低 5）", kindInteger, false, false, nil},
	"WEIBO_COOKIE":                  {"WEIBO_COOKIE", "微博", "weibo.com 登录态（Cookie）", "不是微博密码。浏览器登录后自动写入的一长串会话凭证；也可从已登录的 weibo.com 开发者工具复制。留空保持现有值", kindSecret, false, false, nil},
	"WEIBO_MWEIBO_COOKIE":           {"WEIBO_MWEIBO_COOKIE", "微博", "m.weibo.cn 登录态（Cookie）", "不是密码。手机站会话凭证，用于部分接口/日报；开启浏览器登录后通常会自动同步。留空保持现有值", kindSecret, false, false, nil},
	"WEIBO_SUPER_AUTO_ENABLED":      {"WEIBO_SUPER_AUTO_ENABLED", "微博", "超话自动签到", "全局开关：每日对统一「超话订阅」中已勾选自动签到的超话执行签到", kindBoolean, false, false, nil},
	"WEIBO_SUPER_COUNT_ENABLED":     {"WEIBO_SUPER_COUNT_ENABLED", "微博", "超话日报", "全局开关：每日统计统一「超话订阅」中已勾选日报的超话，并按分组推送", kindBoolean, false, false, nil},
	"WEIBO_SUPER_COUNT_DELIVERY":    {"WEIBO_SUPER_COUNT_DELIVERY", "微博", "日报发送渠道", "email=仅邮件（默认），qq=仅QQ，both=邮件+QQ", kindString, false, false, nil},
	"WEIBO_SUPER_COUNT_QQ":          {"WEIBO_SUPER_COUNT_QQ", "微博", "日报额外 QQ 号", "仅渠道含 QQ 时生效。留空则只发给管理员；多个用逗号/空格/换行分隔", kindString, false, false, nil},
	// 抖音
	"DOUYIN_ENABLED":                     {"DOUYIN_ENABLED", "抖音", "启用抖音", "作品、直播与 IM 总开关", kindBoolean, false, false, nil},
	"DOUYIN_POLL_SECONDS":                {"DOUYIN_POLL_SECONDS", "抖音", "作品轮询间隔", "秒，建议 ≥ 60", kindInteger, false, false, nil},
	"DOUYIN_LIVE_SUMMARY_ENABLED":        {"DOUYIN_LIVE_SUMMARY_ENABLED", "抖音", "下播数据汇总", "下播通知附带监测时长、最高在线及可靠的场观/礼物数据", kindBoolean, false, false, nil},
	"DOUYIN_LIVE_SOUND_WAVE_ENABLED":     {"DOUYIN_LIVE_SOUND_WAVE_ENABLED", "抖音", "采集礼物钻石", "兼容旧配置名；仅累计协议 gift.diamondCount，不称为音浪或收入", kindBoolean, false, false, nil},
	"DOUYIN_LIVE_COOKIE_KEYRING_ACCOUNT": {"DOUYIN_LIVE_COOKIE_KEYRING_ACCOUNT", "抖音", "直播 Cookie 凭据名称", "可选；Cookie 本身只能用 pocket48-douyin-cookie 写入系统凭据库", kindString, false, false, nil},
	"DOUYIN_LIVE_RAW_STATS_DEBUG":        {"DOUYIN_LIVE_RAW_STATS_DEBUG", "抖音", "保存直播统计调试样本", "默认关闭；开启后写入脱敏统计样本", kindBoolean, false, false, nil},
	"DOUYIN_LIVE_RAW_GIFT_DEBUG":         {"DOUYIN_LIVE_RAW_GIFT_DEBUG", "抖音", "保存礼物调试样本", "默认关闭；开启后写入脱敏礼物样本", kindBoolean, false, false, nil},
	"DOUYIN_IM_ENABLED":                  {"DOUYIN_IM_ENABLED", "抖音", "群聊转发", "将指定抖音群消息转到 QQ", kindBoolean, false, false, nil},
	"DOUYIN_IM_PRIVATE_ENABLED":          {"DOUYIN_IM_PRIVATE_ENABLED", "抖音", "私信提醒", "私信通知到默认通知群", kindBoolean, false, false, nil},
	"DOUYIN_IM_GROUP_NAME":               {"DOUYIN_IM_GROUP_NAME", "抖音", "目标群名（辅助）", "可选展示名；多群时优先用抖音回传的群名", kindString, false, false, nil},
	"DOUYIN_IM_GROUP_NUMBER":             {"DOUYIN_IM_GROUP_NUMBER", "抖音", "目标群号（可多个）", "要转发的抖音群号，多个用逗号/空格/换行分隔。与「创作者订阅」独立：那边监控作品/开播，这里监控群主发言", kindString, false, false, nil},
	// 小红书
	"XIAOHONGSHU_ENABLED":      {"XIAOHONGSHU_ENABLED", "小红书", "启用小红书", "帖子监控与可用时的开播提醒", kindBoolean, false, false, nil},
	"XIAOHONGSHU_POLL_SECONDS": {"XIAOHONGSHU_POLL_SECONDS", "小红书", "帖子轮询间隔", "秒，最低 30", kindInteger, false, false, nil},
}

var configGroupOrder = []string{"Bot", "口袋48", "微博", "抖音", "小红书"}

var configFieldOrder = []string{
	"NAPCAT_WS_URL", "NAPCAT_ACCESS_TOKEN", "BOUND_GROUP_ID", "COMMAND_PREFIX", "DISABLE_GROUP_COMMANDS", "MEDIA_DELIVERY",
	"ALERT_EMAIL_ENABLED", "ALERT_EMAIL_TO", "ALERT_EMAIL_FROM", "ALERT_EMAIL_SMTP_HOST", "ALERT_EMAIL_SMTP_PORT",
	"ALERT_EMAIL_SMTP_USER", "ALERT_EMAIL_SMTP_PASSWORD", "ALERT_EMAIL_COOLDOWN_MINUTES", "ADMIN_PANEL_URL",
	"POCKET_USERNAME", "POCKET_PASSWORD", "POCKET_TOKEN", "LIVE_MONITORING", "POLLING_INTERVAL",
	"NIM_ENABLED", "NIM_ROOM_MESSAGE_ENABLED", "NIM_ROOM_MESSAGE_POLL_FALLBACK", "NIM_LIVE_DANMAKU_ENABLED", "NIM_VIEWER_EVENT_ENABLED",
	"WEIBO_BROWSER_AUTH_ENABLED", "WEIBO_BROWSER_REFRESH_MINUTES", "WEIBO_COOKIE", "WEIBO_MWEIBO_COOKIE",
	"WEIBO_SUPER_AUTO_ENABLED", "WEIBO_SUPER_COUNT_ENABLED", "WEIBO_SUPER_COUNT_DELIVERY", "WEIBO_SUPER_COUNT_QQ",
	"DOUYIN_ENABLED", "DOUYIN_POLL_SECONDS", "DOUYIN_LIVE_SUMMARY_ENABLED", "DOUYIN_LIVE_SOUND_WAVE_ENABLED",
	"DOUYIN_LIVE_RAW_STATS_DEBUG", "DOUYIN_LIVE_RAW_GIFT_DEBUG", "DOUYIN_LIVE_COOKIE_KEYRING_ACCOUNT", "DOUYIN_IM_ENABLED", "DOUYIN_IM_PRIVATE_ENABLED",
	"DOUYIN_IM_GROUP_NUMBER", "DOUYIN_IM_GROUP_NAME",
	"XIAOHONGSHU_ENABLED", "XIAOHONGSHU_POLL_SECONDS",
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}

	groups := make(map[string][]configField)
	for _, name := range configGroupOrder {
		groups[name] = []configField{}
	}

	for _, key := range configFieldOrder {
		field := configFields[key]
		field.Value = raw[key]
		if field.Kind == kindSecret && raw[key] != "" && raw[key] != nil {
			field.Configured = true
			field.Value = ""
		}
		if field.Kind == kindBoolean && raw[key] == nil {
			field.Value = key == "DOUYIN_LIVE_SUMMARY_ENABLED" || key == "DOUYIN_LIVE_SOUND_WAVE_ENABLED"
		}
		field.RestartRequired = configFieldNeedsRestart(key)
		groups[field.Group] = append(groups[field.Group], field)
	}

	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{
			"groups":     groups,
			"groupOrder": configGroupOrder,
		})
		return
	}

	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}

	var body struct {
		Values map[string]any `json:"values"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
		return
	}

	validated := make(map[string]any)
	for key, rawValue := range body.Values {
		field, ok := configFields[key]
		if !ok {
			continue
		}
		value, err := validateConfigValue(field.Kind, rawValue)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: fmt.Sprintf("字段 %s: %s", field.Key, err.Error())})
			return
		}
		if key == "WEIBO_SUPER_COUNT_DELIVERY" {
			mode, ok := value.(string)
			if !ok {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "日报发送渠道：必须是文本"})
				return
			}
			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode == "" {
				mode = "email"
			}
			if mode != "email" && mode != "qq" && mode != "both" {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "日报发送渠道：只能是 email / qq / both"})
				return
			}
			value = mode
		}
		if key == "MEDIA_DELIVERY" {
			mode, ok := value.(string)
			if !ok {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "媒体发送方式：必须是文本"})
				return
			}
			mode = strings.ToLower(strings.TrimSpace(mode))
			if mode == "url" || mode == "direct" {
				mode = "remote"
			}
			if mode != "local" && mode != "remote" {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "媒体发送方式：只能是 local 或 remote"})
				return
			}
			value = mode
		}
		if key == "WEIBO_BROWSER_REFRESH_MINUTES" && value.(int64) < 5 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "Cookie 刷新周期：不能小于 5 分钟"})
			return
		}
		validated[key] = value
	}

	// Prefer SIGHUP hot-reload; only force full restart for fields that need process restart.
	needsRestart := false
	for key := range validated {
		if configFieldNeedsRestart(key) {
			needsRestart = true
			break
		}
	}
	if needsRestart {
		if err := s.applyConfig(validated); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "配置已保存，Bot 已重新启动（部分字段需重启）", "restarted": true, "hotReload": false})
		return
	}
	if err := s.writeConfigAndReloadBot(validated); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "配置已保存并热重载（无需重启）", "restarted": false, "hotReload": true})
}

// configFieldNeedsRestart returns true when a key cannot be applied via SIGHUP alone.
// Pocket NIM/live feature flags and poll intervals are read from b.cfg at runtime (same as bot commands),
// so they hot-reload. Only process wiring / credential re-auth / long-lived sidecar spawn need restart.
func configFieldNeedsRestart(key string) bool {
	switch key {
	// Network / process wiring
	case "NAPCAT_WS_URL", "NAPCAT_ACCESS_TOKEN":
		return true
	// Pocket login credentials (token refresh path needs restart for clean re-auth)
	case "POCKET_USERNAME", "POCKET_PASSWORD", "POCKET_TOKEN":
		return true
	// Browser sidecar enable (starts Playwright process)
	case "WEIBO_BROWSER_AUTH_ENABLED":
		return true
	// Platform master switches that start long-lived monitors/sidecars at boot
	case "DOUYIN_ENABLED", "XIAOHONGSHU_ENABLED":
		return true
	// NIM_* / LIVE_MONITORING / POLLING_INTERVAL: hot via cfg (commands already mutate LiveMonitoring)
	default:
		return false
	}
}

func validateConfigValue(kind configKind, rawValue any) (any, error) {
	switch kind {
	case kindString, kindSecret:
		s, ok := rawValue.(string)
		if !ok {
			return nil, errors.New("必须是文本")
		}
		return strings.TrimSpace(s), nil
	case kindBoolean:
		_, ok := rawValue.(bool)
		if !ok {
			return nil, errors.New("必须是开关值")
		}
		return rawValue, nil
	case kindInteger:
		f, ok := rawValue.(float64)
		if !ok {
			return nil, errors.New("必须是整数")
		}
		n := int64(f)
		if n < 0 {
			return nil, errors.New("不能小于 0")
		}
		return n, nil
	case kindStringList:
		list, ok := rawValue.([]any)
		if !ok {
			return nil, errors.New("必须是文本列表")
		}
		result := make([]string, 0, len(list))
		seen := make(map[string]bool)
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s != "" && !seen[s] {
				seen[s] = true
				result = append(result, s)
			}
		}
		return result, nil
	default:
		return nil, errors.New("未知配置类型")
	}
}

// writeConfigNoRestart merges keys into config.json without stopping the bot.
func (s *Server) writeConfigNoRestart(values map[string]any) error {
	var config map[string]any
	if err := readJSONFile(s.opts.ConfigPath, &config); err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	for key, value := range values {
		config[key] = value
	}
	if err := writeJSONAtomic(s.opts.ConfigPath, config); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}

// writeConfigAndReloadBot writes config.json without restart, then signals the bot to hot-reload subscriptions.
func (s *Server) writeConfigAndReloadBot(values map[string]any) error {
	if err := s.writeConfigNoRestart(values); err != nil {
		return err
	}
	if s.reloadSignal != nil {
		if err := s.reloadSignal(); err != nil {
			return fmt.Errorf("配置已保存，但通知 Bot 热重载失败，请重试或手动重启: %w", err)
		}
		return nil
	}
	// CRITICAL: only signal the MAIN pocket48-bot process.
	// `systemctl kill -s HUP` defaults to the whole cgroup and also HUP's chrome/node,
	// which closes the Playwright browserContext (2026-07-20 outage: browser closed → XHS fail alerts).
	_, primaryErr := runCommand(5*time.Second, "systemctl", "kill", "-s", "HUP", "--kill-whom=main", botService)
	if primaryErr != nil {
		// Fallback: kill only MainPID if systemd version lacks --kill-whom
		if out, showErr := runCommand(5*time.Second, "systemctl", "show", "-p", "MainPID", "--value", botService); showErr == nil {
			pid := strings.TrimSpace(out)
			if pid != "" && pid != "0" {
				if _, fallbackErr := runCommand(5*time.Second, "kill", "-s", "HUP", pid); fallbackErr != nil {
					return fmt.Errorf("配置已保存，但通知 Bot 热重载失败，请重试或手动重启: systemd=%v, fallback=%v", primaryErr, fallbackErr)
				}
				return nil
			} else {
				return fmt.Errorf("配置已保存，但通知 Bot 热重载失败，请重试或手动重启: %v (MainPID unavailable)", primaryErr)
			}
		} else {
			return fmt.Errorf("配置已保存，但通知 Bot 热重载失败，请重试或手动重启: systemd=%v, MainPID lookup=%v", primaryErr, showErr)
		}
	}
	return nil
}

func (s *Server) applyConfig(values map[string]any) error {
	if _, err := runCommand(35*time.Second, "systemctl", "stop", botService); err != nil {
		return fmt.Errorf("停止 Bot 失败: %w", err)
	}
	defer func() {
		if _, err := runCommand(35*time.Second, "systemctl", "start", botService); err != nil {
			log.Printf("restart bot after config update: %v", err)
		}
	}()
	var config map[string]any
	if err := readJSONFile(s.opts.ConfigPath, &config); err != nil {
		return fmt.Errorf("读取配置失败: %w", err)
	}
	for key, value := range values {
		config[key] = value
	}
	if err := writeJSONAtomic(s.opts.ConfigPath, config); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "    ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		temp.Close()
		return err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = temp.Chown(int(stat.Uid), int(stat.Gid))
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if output, err := runCommand(45*time.Second, "systemctl", "restart", botService); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: strings.TrimSpace(output + " " + err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Bot 已重新启动"})
}
