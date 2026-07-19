package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type configKind string

const (
	kindString     configKind = "string"
	kindSecret     configKind = "secret"
	kindBoolean    configKind = "boolean"
	kindInteger    configKind = "integer"
	kindStringList configKind = "stringList"
)

type configField struct {
	Key         string     `json:"key"`
	Group       string     `json:"group"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	Kind        configKind `json:"kind"`
	Value       any        `json:"value"`
	Configured  bool       `json:"configured,omitempty"`
}

type fieldDefinition struct {
	Group       string
	Label       string
	Description string
	Kind        configKind
}

// Groups: Bot 本体 → 各平台（口袋/微博/抖音/小红书）。不暴露 headless、侧卡路径、NIM 账号等运维内部项。
var editableConfig = map[string]fieldDefinition{
	// Bot
	"NAPCAT_WS_URL":                {"Bot", "QQ / NapCat 地址", "OneBot WebSocket，例如 ws://127.0.0.1:3001", kindString},
	"NAPCAT_ACCESS_TOKEN":          {"Bot", "NapCat Token", "留空保持现有值", kindSecret},
	"BOUND_GROUP_ID":               {"Bot", "默认通知群", "系统通知、抖音私信等默认转发的 QQ 群号", kindInteger},
	"COMMAND_PREFIX":               {"Bot", "命令前缀", "群内机器人命令前缀，默认 bot", kindString},
	"DISABLE_GROUP_COMMANDS":       {"Bot", "禁用群命令", "关闭所有群聊命令响应", kindBoolean},
	"MEDIA_DELIVERY":               {"Bot", "媒体发送方式", "local=本机下载后发给 QQ（推荐）；remote=直链交给 NapCat 下载", kindString},
	"ALERT_EMAIL_ENABLED":          {"Bot", "邮件告警", "仅持续异常、需人工处理时发信；可自愈/已恢复不发", kindBoolean},
	"ALERT_EMAIL_TO":               {"Bot", "收件邮箱", "接收告警与超话日报的邮箱", kindString},
	"ALERT_EMAIL_FROM":             {"Bot", "发件邮箱", "发件人地址（SMTP 登录或本机 sendmail 的 From）", kindString},
	"ALERT_EMAIL_SMTP_HOST":        {"Bot", "SMTP 服务器", "例如 smtp.qq.com；Windows 必填。留空则尝试本机 sendmail（Linux）", kindString},
	"ALERT_EMAIL_SMTP_PORT":        {"Bot", "SMTP 端口", "常用 465（SSL）或 587（STARTTLS）", kindInteger},
	"ALERT_EMAIL_SMTP_USER":        {"Bot", "SMTP 用户名", "多数邮箱填完整邮箱地址；留空则用发件邮箱", kindString},
	"ALERT_EMAIL_SMTP_PASSWORD":    {"Bot", "SMTP 密码/授权码", "QQ 邮箱请填授权码而非登录密码；留空保持现有值", kindSecret},
	"ALERT_EMAIL_COOLDOWN_MINUTES": {"Bot", "告警冷却（分钟）", "同一服务重复告警的最短间隔", kindInteger},
	"ADMIN_PANEL_URL":              {"Bot", "管理面板链接", "可选，写入告警邮件按钮；例 https://panel.example.com 留空则邮件不带按钮", kindString},

	// 口袋48
	"POCKET_USERNAME":                {"口袋48", "账号", "手机号或口袋账号", kindString},
	"POCKET_PASSWORD":                {"口袋48", "密码", "Token 失效时用于重新登录；留空保持现有值", kindSecret},
	"POCKET_TOKEN":                   {"口袋48", "Token", "可粘贴已有 Token；留空保持现有值", kindSecret},
	"LIVE_MONITORING":                {"口袋48", "直播监控", "成员开播/下播通知", kindBoolean},
	"POLLING_INTERVAL":               {"口袋48", "REST 轮询间隔", "QChat 断线或不完整时的补洞间隔（秒）", kindInteger},
	"NIM_ENABLED":                    {"口袋48", "启用实时通道", "云信/QChat 实时事件（建议开）", kindBoolean},
	"NIM_ROOM_MESSAGE_ENABLED":       {"口袋48", "实时房间消息", "用 QChat 收房间消息", kindBoolean},
	"NIM_ROOM_MESSAGE_POLL_FALLBACK": {"口袋48", "REST 补洞", "实时不完整或断线时用 REST 补偿（建议开）", kindBoolean},
	"NIM_LIVE_DANMAKU_ENABLED":       {"口袋48", "直播弹幕转发", "转发其他小偶像的弹幕", kindBoolean},
	"NIM_VIEWER_EVENT_ENABLED":       {"口袋48", "进出房事件", "转发其他小偶像进出直播间", kindBoolean},

	// 微博（Cookie 等在下方订阅区也可维护；浏览器侧卡路径不暴露）
	"WEIBO_BROWSER_AUTH_ENABLED":    {"微博", "浏览器登录", "用面板浏览器页扫码维护 Cookie（与抖音/小红书共用）", kindBoolean},
	"WEIBO_BROWSER_REFRESH_MINUTES": {"微博", "Cookie 刷新周期", "自动刷新间隔（分钟，最低 5）", kindInteger},
	"WEIBO_COOKIE":                  {"微博", "weibo.com Cookie", "可手动粘贴；留空保持现有值", kindSecret},
	"WEIBO_MWEIBO_COOKIE":           {"微博", "m.weibo Cookie", "可选；留空保持现有值", kindSecret},
	"WEIBO_SUPER_AUTO_ENABLED":      {"微博", "超话自动签到", "每日自动超话签到", kindBoolean},
	"WEIBO_SUPER_COUNT_ENABLED":     {"微博", "超话日报", "每日超话签到人数统计与邮件", kindBoolean},

	// 抖音
	"DOUYIN_ENABLED":            {"抖音", "启用抖音", "作品、直播与 IM 总开关", kindBoolean},
	"DOUYIN_POLL_SECONDS":       {"抖音", "作品轮询间隔", "秒，建议 ≥ 60", kindInteger},
	"DOUYIN_IM_ENABLED":         {"抖音", "群聊转发", "将指定抖音群消息转到 QQ", kindBoolean},
	"DOUYIN_IM_PRIVATE_ENABLED": {"抖音", "私信提醒", "私信通知到默认通知群", kindBoolean},
	"DOUYIN_IM_GROUP_NAME":      {"抖音", "目标群名（辅助）", "仅当群号为空时用名称匹配一个抖音群；多群请优先填群号", kindString},
	"DOUYIN_IM_GROUP_NUMBER":    {"抖音", "目标群号", "精确匹配要转发的抖音群号（推荐）", kindString},

	// 小红书
	"XIAOHONGSHU_ENABLED":      {"小红书", "启用小红书", "帖子监控与可用时的开播提醒", kindBoolean},
	"XIAOHONGSHU_POLL_SECONDS": {"小红书", "帖子轮询间隔", "秒，最低 30", kindInteger},
}

var configGroupOrder = []string{"Bot", "口袋48", "微博", "抖音", "小红书"}

var configFieldOrder = []string{
	"NAPCAT_WS_URL", "NAPCAT_ACCESS_TOKEN", "BOUND_GROUP_ID", "COMMAND_PREFIX", "DISABLE_GROUP_COMMANDS", "MEDIA_DELIVERY",
	"ALERT_EMAIL_ENABLED", "ALERT_EMAIL_TO", "ALERT_EMAIL_FROM", "ALERT_EMAIL_SMTP_HOST", "ALERT_EMAIL_SMTP_PORT",
	"ALERT_EMAIL_SMTP_USER", "ALERT_EMAIL_SMTP_PASSWORD", "ALERT_EMAIL_COOLDOWN_MINUTES", "ADMIN_PANEL_URL",
	"POCKET_USERNAME", "POCKET_PASSWORD", "POCKET_TOKEN", "LIVE_MONITORING", "POLLING_INTERVAL",
	"NIM_ENABLED", "NIM_ROOM_MESSAGE_ENABLED", "NIM_ROOM_MESSAGE_POLL_FALLBACK", "NIM_LIVE_DANMAKU_ENABLED", "NIM_VIEWER_EVENT_ENABLED",
	"WEIBO_BROWSER_AUTH_ENABLED", "WEIBO_BROWSER_REFRESH_MINUTES", "WEIBO_COOKIE", "WEIBO_MWEIBO_COOKIE",
	"WEIBO_SUPER_AUTO_ENABLED", "WEIBO_SUPER_COUNT_ENABLED",
	"DOUYIN_ENABLED", "DOUYIN_POLL_SECONDS", "DOUYIN_IM_ENABLED", "DOUYIN_IM_PRIVATE_ENABLED",
	"DOUYIN_IM_GROUP_NUMBER", "DOUYIN_IM_GROUP_NAME",
	"XIAOHONGSHU_ENABLED", "XIAOHONGSHU_POLL_SECONDS",
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getConfig(w)
	case http.MethodPut:
		s.putConfig(w, r)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) getConfig(w http.ResponseWriter) {
	var raw map[string]any
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	groups := make(map[string][]configField)
	orderIndex := map[string]int{}
	for i, key := range configFieldOrder {
		orderIndex[key] = i
	}
	for key, definition := range editableConfig {
		value := raw[key]
		field := configField{Key: key, Group: definition.Group, Label: definition.Label, Description: definition.Description, Kind: definition.Kind}
		if definition.Kind == kindSecret {
			field.Value = ""
			field.Configured = strings.TrimSpace(fmt.Sprint(value)) != ""
		} else {
			field.Value = value
		}
		groups[definition.Group] = append(groups[definition.Group], field)
	}
	for _, fields := range groups {
		sort.Slice(fields, func(i, j int) bool {
			ai, aok := orderIndex[fields[i].Key]
			bi, bok := orderIndex[fields[j].Key]
			if aok && bok {
				return ai < bi
			}
			return fields[i].Label < fields[j].Label
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "groupOrder": configGroupOrder})
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "配置请求格式无效"})
		return
	}
	if len(body.Values) == 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "没有需要保存的配置"})
		return
	}
	validated := make(map[string]any, len(body.Values))
	for key, encoded := range body.Values {
		definition, ok := editableConfig[key]
		if !ok {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "不允许修改配置项 " + key})
			return
		}
		value, err := validateConfigValue(definition.Kind, encoded)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: definition.Label + "：" + err.Error()})
			return
		}
		if definition.Kind == kindSecret && value == "" {
			continue
		}
		if key == "XIAOHONGSHU_POLL_SECONDS" && value.(int64) < 30 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "帖子轮询间隔：不能小于 30 秒"})
			return
		}
		if key == "MEDIA_DELIVERY" {
			mode := strings.ToLower(strings.TrimSpace(value.(string)))
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
	if err := s.applyConfig(validated); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "配置已保存，Bot 已重新启动"})
}

func validateConfigValue(kind configKind, encoded json.RawMessage) (any, error) {
	switch kind {
	case kindString, kindSecret:
		var value string
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, errors.New("必须是文本")
		}
		return strings.TrimSpace(value), nil
	case kindBoolean:
		var value bool
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, errors.New("必须是开关值")
		}
		return value, nil
	case kindInteger:
		var value int64
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, errors.New("必须是整数")
		}
		if value < 0 {
			return nil, errors.New("不能小于 0")
		}
		return value, nil
	case kindStringList:
		var value []string
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, errors.New("必须是文本列表")
		}
		result := make([]string, 0, len(value))
		seen := make(map[string]bool)
		for _, item := range value {
			item = strings.TrimSpace(item)
			if item != "" && !seen[item] {
				seen[item] = true
				result = append(result, item)
			}
		}
		return result, nil
	default:
		return nil, errors.New("未知配置类型")
	}
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
