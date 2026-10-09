package logic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// readBiliSettingsCookie 从项目 storage 里读真实 Cookie（仅测试用）。
func readBiliSettingsCookie() (string, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "storage", "bilibili", "settings.json"))
	if err != nil {
		return "", err
	}
	var cfg struct {
		Cookie string `json:"cookie"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", err
	}
	return strings.TrimSpace(cfg.Cookie), nil
}
