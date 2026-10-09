package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWeiboBrowserStatusGETExists 锁住 GET 分支。
//
// 背景：/api/browser/weibo 早期只注册了 POST，前端「浏览器」页面每 10 秒
// 轮询一次 sessionConfigured，微博这一请求拿到 405，异常被 catch 吞掉后
// 一律按「未登录」渲染 —— 哪怕微博 cookie 完好、服务正常，状态灯也永远
// 亮不起来。这个测试就是防止有人再次把 GET 分支删掉。
func TestWeiboBrowserStatusGETExists(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"WEIBO_COOKIE":"SUB=abc","WEIBO_MWEIBO_COOKIE":"MLOGIN=1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{opts: Options{ConfigPath: configPath}}
	rr := httptest.NewRecorder()
	s.handleBrowserWeibo(rr, httptest.NewRequest(http.MethodGet, "/api/browser/weibo", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET 应返回 200，实际 %d：%s", rr.Code, rr.Body.String())
	}
	var status struct {
		SessionConfigured bool `json:"sessionConfigured"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, rr.Body.String())
	}
	if !status.SessionConfigured {
		t.Fatalf("配置里有微博 cookie 时 sessionConfigured 应为 true")
	}
	// 绝不能把 cookie 值漏给前端。
	if strings.Contains(rr.Body.String(), "SUB=abc") || strings.Contains(rr.Body.String(), "MLOGIN=1") {
		t.Fatalf("响应泄露了 cookie 值: %s", rr.Body.String())
	}
}

// TestWeiboBrowserStatusWithoutCookies 反向断言：没有 cookie 必须是 false，
// 不能一律给 true 糊弄过去。
func TestWeiboBrowserStatusWithoutCookies(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"WEIBO_COOKIE":"  ","WEIBO_MWEIBO_COOKIE":""}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{opts: Options{ConfigPath: configPath}}
	rr := httptest.NewRecorder()
	s.handleBrowserWeibo(rr, httptest.NewRequest(http.MethodGet, "/api/browser/weibo", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("GET 应返回 200，实际 %d", rr.Code)
	}
	var status struct {
		SessionConfigured bool `json:"sessionConfigured"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.SessionConfigured {
		t.Fatalf("cookie 为空时 sessionConfigured 应为 false")
	}
}

// TestWeiboBrowserRejectsOtherMethods 确认非 GET/POST 仍被拒绝。
func TestWeiboBrowserRejectsOtherMethods(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	_ = os.WriteFile(configPath, []byte(`{}`), 0o600)

	s := &Server{opts: Options{ConfigPath: configPath}}
	rr := httptest.NewRecorder()
	s.handleBrowserWeibo(rr, httptest.NewRequest(http.MethodDelete, "/api/browser/weibo", nil))
	if rr.Code == http.StatusOK {
		t.Fatalf("DELETE 不应被接受")
	}
}
