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

var editableConfig = map[string]fieldDefinition{
	"BOUND_GROUP_ID":                 {"基础", "默认转发群", "抖音 IM 与系统通知的目标 QQ 群", kindInteger},
	"POLLING_INTERVAL":               {"基础", "REST 轮询间隔", "QChat 不可用时的轮询间隔（秒）", kindInteger},
	"COMMAND_PREFIX":                 {"基础", "命令前缀", "机器人命令触发前缀", kindString},
	"DISABLE_GROUP_COMMANDS":         {"基础", "禁用群命令", "关闭所有群聊命令响应", kindBoolean},
	"LIVE_MONITORING":                {"基础", "直播监控", "启用口袋48直播开播与结束通知", kindBoolean},
	"NAPCAT_WS_URL":                  {"连接", "NapCat WebSocket", "OneBot WebSocket 地址", kindString},
	"NAPCAT_ACCESS_TOKEN":            {"连接", "NapCat Token", "留空保持现有值", kindSecret},
	"POCKET_USERNAME":                {"连接", "口袋48账号", "登录手机号或账号", kindString},
	"POCKET_PASSWORD":                {"连接", "口袋48密码", "留空保持现有值", kindSecret},
	"POCKET_TOKEN":                   {"连接", "口袋48 Token", "留空保持现有值", kindSecret},
	"NIM_ENABLED":                    {"QChat", "启用 NIM", "启用云信实时事件桥接", kindBoolean},
	"NIM_ROOM_MESSAGE_ENABLED":       {"QChat", "实时房间消息", "使用 QChat WebSocket 接收房间消息", kindBoolean},
	"NIM_ROOM_MESSAGE_POLL_FALLBACK": {"QChat", "REST 兜底", "实时消息不完整时允许 REST 补偿", kindBoolean},
	"NIM_LIVE_DANMAKU_ENABLED":       {"QChat", "直播弹幕", "接收成员直播弹幕事件", kindBoolean},
	"NIM_VIEWER_EVENT_ENABLED":       {"QChat", "观众事件", "接收进房与离房事件", kindBoolean},
	"NIM_ACCOUNT":                    {"QChat", "NIM Account", "云信账号标识", kindString},
	"NIM_TOKEN":                      {"QChat", "NIM Token", "留空保持现有值", kindSecret},
	"NIM_SIDECAR_CMD":                {"QChat", "NIM 侧卡命令", "Node QChat 桥接启动命令", kindString},
	"WEIBO_BROWSER_AUTH_ENABLED":     {"浏览器", "微博浏览器认证", "启用持久化浏览器认证", kindBoolean},
	"BROWSER_SIDECAR_CMD":            {"浏览器", "统一浏览器侧卡", "微博与抖音共用浏览器启动命令", kindString},
	"BROWSER_PROFILE_DIR":            {"浏览器", "浏览器 Profile", "持久化登录态目录", kindString},
	"BROWSER_HEADLESS":               {"浏览器", "无头模式", "关闭后使用 Xvfb 正常窗口运行", kindBoolean},
	"WEIBO_BROWSER_REFRESH_MINUTES":  {"浏览器", "微博刷新周期", "认证 Cookie 自动刷新间隔（分钟）", kindInteger},
	"DOUYIN_ENABLED":                 {"抖音", "抖音监控", "启用作品、直播和 IM 模块", kindBoolean},
	"DOUYIN_POLL_SECONDS":            {"抖音", "作品轮询间隔", "作品检查周期（秒）", kindInteger},
	"DOUYIN_IM_ENABLED":              {"抖音", "群聊 IM", "启用抖音群聊只读连接", kindBoolean},
	"DOUYIN_IM_PRIVATE_ENABLED":      {"抖音", "私信提醒", "将收到的抖音私信通知管理员", kindBoolean},
	"DOUYIN_IM_GROUP_NAME":           {"抖音", "目标群名", "用于识别目标抖音群", kindString},
	"DOUYIN_IM_GROUP_NUMBER":         {"抖音", "目标群号", "精确匹配目标抖音群", kindString},
	"DOUYIN_LIVE_WS_URL":             {"抖音", "直播 WebSocket", "抖音直播事件服务地址", kindString},
	"DOUYIN_LIVE_SIDECAR_CMD":        {"抖音", "直播侧卡命令", "留空表示使用外部服务", kindString},
	"XIAOHONGSHU_ENABLED":            {"小红书", "小红书监控", "启用帖子监控与可用时的开播提醒", kindBoolean},
	"XIAOHONGSHU_POLL_SECONDS":       {"小红书", "帖子轮询间隔", "个人主页检查周期（秒，最低 30 秒）", kindInteger},
	"ALERT_EMAIL_ENABLED":            {"告警", "邮件告警", "服务持续异常且超过自愈窗口后才发邮件；可自愈/已恢复不发", kindBoolean},
	"ALERT_EMAIL_TO":                 {"告警", "收件邮箱", "接收需人工处理的服务异常邮件", kindString},
	"ALERT_EMAIL_FROM":               {"告警", "发件地址", "建议使用 jiufeng.cloud 域名地址", kindString},
	"ALERT_EMAIL_COOLDOWN_MINUTES":   {"告警", "告警冷却", "同一服务重复异常告警的最短间隔（分钟）", kindInteger},
}

var configGroupOrder = []string{"基础", "连接", "QChat", "浏览器", "抖音", "小红书", "告警"}

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
		sort.Slice(fields, func(i, j int) bool { return fields[i].Label < fields[j].Label })
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
