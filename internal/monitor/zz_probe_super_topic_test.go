package monitor

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// 探针：确认「超话页面信息接口」能独立取到签到数（监测功能的数据源）。
//
// 跑法：POCKET48_WB_PROBE=1 go test ./internal/monitor/ -run TestZZSuperTopicSignProbe -v
func TestZZSuperTopicSignProbe(t *testing.T) {
	if os.Getenv("POCKET48_WB_PROBE") == "" {
		t.Skip("未设置 POCKET48_WB_PROBE")
	}
	raw, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Skipf("读不到 config.json: %v", err)
	}
	var cfg struct {
		WeiboApp struct {
			RawCapture     string `json:"raw_capture"`
			Host           string `json:"host"`
			RequestPath    string `json:"request_path"`
			RequestBody    string `json:"request_body"`
			CapturedOID    string `json:"captured_oid"`
			Authorization  string `json:"authorization"`
			Gsid           string `json:"gsid"`
			Aid            string `json:"aid"`
			S              string `json:"s"`
			XSessionID     string `json:"x-sessionid"`
			XValidator     string `json:"x-validator"`
			XShanhaiPass   string `json:"x-shanhai-pass"`
			XLogUID        string `json:"x-log-uid"`
			XEngineType    string `json:"x-engine-type"`
			CronetRID      string `json:"cronet_rid"`
			SNRT           string `json:"snrt"`
			AcceptLanguage string `json:"accept_language"`
			AcceptEncoding string `json:"accept_encoding"`
			UserAgent      string `json:"ua"`
		} `json:"WEIBO_APP"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config 解析失败: %v", err)
	}
	app := &WeiboAppAuth{
		RawCapture: cfg.WeiboApp.RawCapture, Host: cfg.WeiboApp.Host,
		RequestPath: cfg.WeiboApp.RequestPath, RequestBody: cfg.WeiboApp.RequestBody,
		CapturedOID: cfg.WeiboApp.CapturedOID, Authorization: cfg.WeiboApp.Authorization,
		GSID: cfg.WeiboApp.Gsid, Aid: cfg.WeiboApp.Aid, S: cfg.WeiboApp.S,
		XSessionID: cfg.WeiboApp.XSessionID, XValidator: cfg.WeiboApp.XValidator,
		XShanhaiPass: cfg.WeiboApp.XShanhaiPass, XLogUID: cfg.WeiboApp.XLogUID,
		XEngineType: cfg.WeiboApp.XEngineType, CronetRID: cfg.WeiboApp.CronetRID,
		SNRT: cfg.WeiboApp.SNRT, AcceptLanguage: cfg.WeiboApp.AcceptLanguage,
		AcceptEncoding: cfg.WeiboApp.AcceptEncoding, UserAgent: cfg.WeiboApp.UserAgent,
	}
	if app.Authorization == "" {
		t.Skip("config.json 里没有 WEIBO_APP.authorization")
	}
	m := &WeiboMonitor{configs: map[int64]map[string]*WeiboConfig{}}
	m.SetAppAuth(app)

	for _, tc := range []struct{ name, oid string }{
		{"ChoiJiwoo", "100808c35fb628604a4ed52a30bf6a3b345c03"},
		{"stella", "100808430244540aa38ff181545cb0d539f038"},
	} {
		start := time.Now()
		res, err := m.fetchSuperCountByOIDViaApp(tc.oid, tc.name)
		cost := time.Since(start)
		if err != nil {
			t.Logf("[%s] 抓取失败 (%.2fs): %v", tc.name, cost.Seconds(), err)
			continue
		}
		t.Logf("[%s] %.2fs name=%q sign_count=%d sign_text=%q level=%q superLike=%d",
			tc.name, cost.Seconds(), res.Name, res.SignCount, res.SignText, res.LevelText, res.SuperLikeCount)
	}
}
