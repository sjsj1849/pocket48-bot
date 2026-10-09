package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSidecar 起一个假的浏览器侧卡，回调由调用方给。
func fakeSidecar(t *testing.T, reply func(cmd string) (resultType string, cookies []browserSnapshotCookie, errMsg string)) int {
	t.Helper()
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var cmd map[string]string
		if conn.ReadJSON(&cmd) != nil {
			return
		}
		resultType, cookies, errMsg := reply(cmd["cmd"])
		payload := map[string]any{"type": resultType, "requestId": cmd["requestId"]}
		if errMsg != "" {
			payload["error"] = errMsg
		} else if cookies != nil {
			payload["session"] = map[string]any{"cookies": cookies}
		}
		_ = conn.WriteJSON(payload)
	}))
	t.Cleanup(sidecar.Close)
	endpoint, _ := url.Parse(sidecar.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	return port
}

// newLoginTestServer 造一个带 config.json 与侧卡端口的 Server。
func newLoginTestServer(t *testing.T, port int) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "storage"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]int{"port": port})
	if err := os.WriteFile(filepath.Join(dir, "storage", "browser-sidecar.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Server{opts: Options{ConfigPath: filepath.Join(dir, "config.json")}}
}

func douyinLoginCookie() browserSnapshotCookie {
	return browserSnapshotCookie{
		Name: "sessionid", Value: "abc123", Domain: ".douyin.com", Expires: -1,
	}
}

func tiktokLoginCookie() browserSnapshotCookie {
	return browserSnapshotCookie{
		Name: "sessionid", Value: "xyz789", Domain: ".tiktok.com", Expires: -1,
	}
}

// TestBrowserDouyinAndTiktokCommands 确认两个平台的 open/sync 都发对了侧卡命令。
func TestBrowserDouyinAndTiktokCommands(t *testing.T) {
	cases := []struct {
		name    string
		handler func(*Server, http.ResponseWriter, *http.Request)
		want    string
		// cookie 必须是**该平台**的登录 cookie，否则 sync 会因校验不过而报 400。
		cookie browserSnapshotCookie
	}{
		{"douyin", (*Server).handleBrowserDouyin, "douyin_panel_", douyinLoginCookie()},
		{"tiktok", (*Server).handleBrowserTiktok, "tiktok_panel_", tiktokLoginCookie()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := make(chan string, 4)
			port := fakeSidecar(t, func(cmd string) (string, []browserSnapshotCookie, string) {
				got <- cmd
				return c.want + "result", []browserSnapshotCookie{c.cookie}, ""
			})
			s := newLoginTestServer(t, port)
			for _, action := range []string{"open", "sync"} {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/api/browser/"+c.name,
					strings.NewReader(`{"action":"`+action+`"}`))
				c.handler(s, rr, req)
				if rr.Code != http.StatusOK {
					t.Fatalf("%s: %d %s", action, rr.Code, rr.Body.String())
				}
				if cmd := <-got; cmd != c.want+action {
					t.Fatalf("命令应为 %s%s，实际 %s", c.want, action, cmd)
				}
			}
			// 非法 action 必须被拒。
			rr := httptest.NewRecorder()
			c.handler(s, rr, httptest.NewRequest(http.MethodPost, "/api/browser/"+c.name,
				strings.NewReader(`{"action":"shutdown"}`)))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("任意命令被接受: %d", rr.Code)
			}
		})
	}
}

// TestBrowserDouyinSyncPersistsSnapshot 确认 sync 会把登录态落盘。
func TestBrowserDouyinSyncPersistsSnapshot(t *testing.T) {
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "douyin_panel_result", []browserSnapshotCookie{douyinLoginCookie()}, ""
	})
	s := newLoginTestServer(t, port)

	rr := httptest.NewRecorder()
	s.handleBrowserDouyin(rr, httptest.NewRequest(http.MethodPost, "/api/browser/douyin",
		strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("sync 失败: %d %s", rr.Code, rr.Body.String())
	}

	// GET 之后应该是已登录。
	rr = httptest.NewRecorder()
	s.handleBrowserDouyin(rr, httptest.NewRequest(http.MethodGet, "/api/browser/douyin", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET 失败: %d", rr.Code)
	}
	var status map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatalf("GET 返回非 JSON: %v", err)
	}
	if status["sessionConfigured"] != true {
		t.Fatalf("同步后应显示已登录: %s", rr.Body.String())
	}
	// ★ 绝不能把 cookie 内容回给前端。
	if strings.Contains(rr.Body.String(), "abc123") {
		t.Fatalf("GET 泄漏了 cookie 内容: %s", rr.Body.String())
	}
}

// TestBrowserDouyinSyncWithoutLoginFails 未登录时 sync 必须报错且不落盘。
func TestBrowserDouyinSyncWithoutLoginFails(t *testing.T) {
	// 只有 ttwid 这种游客 cookie，没有 sessionid。
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "douyin_panel_result", []browserSnapshotCookie{{
			Name: "ttwid", Value: "anon", Domain: ".douyin.com", Expires: -1,
		}}, ""
	})
	s := newLoginTestServer(t, port)

	rr := httptest.NewRecorder()
	s.handleBrowserDouyin(rr, httptest.NewRequest(http.MethodPost, "/api/browser/douyin",
		strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("未登录应报错，实际 %d %s", rr.Code, rr.Body.String())
	}
	// 快照不该被写出来。
	path := filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "douyin-browser", "session.json")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("未登录时不应写入快照")
	}
}

// TestBrowserTiktokRequiresSessionid 确认 ttwid 不算登录（否则状态灯永远绿）。
func TestBrowserTiktokRequiresSessionid(t *testing.T) {
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "tiktok_panel_result", []browserSnapshotCookie{
			{Name: "ttwid", Value: "anon", Domain: ".tiktok.com", Expires: -1},
			{Name: "msToken", Value: "tok", Domain: ".tiktok.com", Expires: -1},
		}, ""
	})
	s := newLoginTestServer(t, port)

	rr := httptest.NewRecorder()
	s.handleBrowserTiktok(rr, httptest.NewRequest(http.MethodPost, "/api/browser/tiktok",
		strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("只有 ttwid 不该算登录: %d %s", rr.Code, rr.Body.String())
	}
}

// TestBrowserPlatformSidecarError 侧卡报错时要把原文透出。
func TestBrowserPlatformSidecarError(t *testing.T) {
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "tiktok_panel_result", nil, "TikTok 浏览器操作失败，请在下方浏览器重试"
	})
	s := newLoginTestServer(t, port)

	rr := httptest.NewRecorder()
	s.handleBrowserTiktok(rr, httptest.NewRequest(http.MethodPost, "/api/browser/tiktok",
		strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("应报错，实际 %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "下方浏览器重试") {
		t.Fatalf("错误原文未透出: %s", rr.Body.String())
	}
}

// TestBrowserPlatformWithoutSidecar 侧卡没起来时应给出可读提示，而不是 500。
func TestBrowserPlatformWithoutSidecar(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "storage"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: filepath.Join(dir, "config.json")}}

	rr := httptest.NewRecorder()
	s.handleBrowserDouyin(rr, httptest.NewRequest(http.MethodPost, "/api/browser/douyin",
		strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("侧卡缺失应返回 400，实际 %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "侧卡") {
		t.Fatalf("提示应提到侧卡: %s", rr.Body.String())
	}
}

// TestBrowserPlatformGetBeforeLogin GET 在从未同步过时也要正常返回（不能 404/500）。
//
// 前端每 10 秒轮询所有平台，任何一个平台 GET 失败都会被 catch 掉并
// 保持「未登录」—— 但如果 GET 直接 panic 或返回 500，页面上其他平台也会连带受影响。
func TestBrowserPlatformGetBeforeLogin(t *testing.T) {
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "", nil, ""
	})
	s := newLoginTestServer(t, port)

	for _, h := range []func(*Server, http.ResponseWriter, *http.Request){
		(*Server).handleBrowserDouyin, (*Server).handleBrowserTiktok,
	} {
		rr := httptest.NewRecorder()
		h(s, rr, httptest.NewRequest(http.MethodGet, "/api/browser/x", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("未同步过时 GET 应 200，实际 %d %s", rr.Code, rr.Body.String())
		}
		var status map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
			t.Fatalf("GET 返回非 JSON: %v", err)
		}
		if status["sessionConfigured"] != false {
			t.Fatalf("未同步时应为未登录: %s", rr.Body.String())
		}
	}
}

// TestBrowserPlatformRejectsOtherMethods 只允许 GET/POST。
func TestBrowserPlatformRejectsOtherMethods(t *testing.T) {
	s := newLoginTestServer(t, 1)
	for _, h := range []func(*Server, http.ResponseWriter, *http.Request){
		(*Server).handleBrowserDouyin, (*Server).handleBrowserTiktok,
	} {
		rr := httptest.NewRecorder()
		h(s, rr, httptest.NewRequest(http.MethodDelete, "/api/browser/x", nil))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("DELETE 应被拒，实际 %d", rr.Code)
		}
	}
}

// TestBrowserPlatformSnapshotFilePermissions 快照文件必须是 0600。
func TestBrowserPlatformSnapshotFilePermissions(t *testing.T) {
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "douyin_panel_result", []browserSnapshotCookie{douyinLoginCookie()}, ""
	})
	s := newLoginTestServer(t, port)

	rr := httptest.NewRecorder()
	s.handleBrowserDouyin(rr, httptest.NewRequest(http.MethodPost, "/api/browser/douyin",
		strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("sync 失败: %d %s", rr.Code, rr.Body.String())
	}
	dir := filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "douyin-browser")
	info, err := os.Stat(filepath.Join(dir, "session.json"))
	if err != nil {
		t.Fatalf("快照未落盘: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("快照权限应为 0600，实际 %o", mode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("目录不存在: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("目录权限应为 0700，实际 %o", mode)
	}
}

// TestBrowserRoutesRegistered 锁住路由注册。
//
// ★ 这就是「面板浏览器页面只有 X 和微博按钮」的根因（2026-10-04）：
// handler 都写好了，但 server.go 的 switch 里没注册，于是前端请求拿到 404。
// 这个测试直接请求路由表，任何人把 case 删掉都会立刻红。
func TestBrowserRoutesRegistered(t *testing.T) {
	port := fakeSidecar(t, func(string) (string, []browserSnapshotCookie, string) {
		return "", nil, ""
	})
	s := newLoginTestServer(t, port)

	for _, path := range []string{
		"/api/browser/douyin",
		"/api/browser/tiktok",
		"/api/browser/bilibili",
	} {
		rr := httptest.NewRecorder()
		s.handleAPI(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code == http.StatusNotFound {
			t.Fatalf("%s 未注册到路由表", path)
		}
		if rr.Code == http.StatusUnauthorized {
			// 未登录是预期的，不代表路由缺失。
			continue
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
	}
}

// TestValidDouyinSessionCookies 覆盖域名与过期判定。
func TestValidDouyinSessionCookies(t *testing.T) {
	future := float64(timeNowPlus())
	past := float64(timeNowMinus())
	cases := []struct {
		name    string
		cookies []browserSnapshotCookie
		want    bool
	}{
		{"空", nil, false},
		{"有sessionid", []browserSnapshotCookie{{Name: "sessionid", Domain: ".douyin.com", Expires: -1}}, true},
		{"sessionid_ss", []browserSnapshotCookie{{Name: "sessionid_ss", Domain: ".douyin.com", Expires: -1}}, true},
		{"iesdouyin域也算", []browserSnapshotCookie{{Name: "sessionid", Domain: ".iesdouyin.com", Expires: -1}}, true},
		{"只有ttwid", []browserSnapshotCookie{{Name: "ttwid", Domain: ".douyin.com", Expires: -1}}, false},
		{"sessionid过期", []browserSnapshotCookie{{Name: "sessionid", Domain: ".douyin.com", Expires: past}}, false},
		{"sessionid未过期", []browserSnapshotCookie{{Name: "sessionid", Domain: ".douyin.com", Expires: future}}, true},
		{"别的站点的sessionid", []browserSnapshotCookie{{Name: "sessionid", Domain: ".example.com", Expires: -1}}, false},
		// 双点域是畸形输入，但浏览器不会产出它。这里刻意保持宽松：
		// TrimPrefix 只去掉一个点，剩下的 ".douyin.com" 仍以 ".douyin.com" 结尾，
		// 判为命中。收紧它没有收益，只会在 cookie 域名格式有变时误杀真登录态。
		{"双点域仍宽松命中", []browserSnapshotCookie{{Name: "sessionid", Domain: "..douyin.com", Expires: -1}}, true},
		{"子域也算", []browserSnapshotCookie{{Name: "sessionid", Domain: "www.douyin.com", Expires: -1}}, true},
	}
	for _, c := range cases {
		if got := validDouyinSession(douyinBrowserSnapshot{Cookies: c.cookies}); got != c.want {
			t.Errorf("%s: validDouyinSession = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// TestValidTiktokSessionCookies 同上，覆盖 TikTok。
func TestValidTiktokSessionCookies(t *testing.T) {
	future := float64(timeNowPlus())
	past := float64(timeNowMinus())
	cases := []struct {
		name    string
		cookies []browserSnapshotCookie
		want    bool
	}{
		{"空", nil, false},
		{"有sessionid", []browserSnapshotCookie{{Name: "sessionid", Domain: ".tiktok.com", Expires: -1}}, true},
		{"只有ttwid不算", []browserSnapshotCookie{{Name: "ttwid", Domain: ".tiktok.com", Expires: -1}}, false},
		{"sessionid过期", []browserSnapshotCookie{{Name: "sessionid", Domain: ".tiktok.com", Expires: past}}, false},
		{"sessionid未过期", []browserSnapshotCookie{{Name: "sessionid", Domain: ".tiktok.com", Expires: future}}, true},
		{"别的站点", []browserSnapshotCookie{{Name: "sessionid", Domain: ".douyin.com", Expires: -1}}, false},
	}
	for _, c := range cases {
		if got := validTiktokSession(tiktokBrowserSnapshot{Cookies: c.cookies}); got != c.want {
			t.Errorf("%s: validTiktokSession = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func timeNowPlus() int64  { return time.Now().Add(24 * time.Hour).Unix() }
func timeNowMinus() int64 { return time.Now().Add(-24 * time.Hour).Unix() }

func TestWriteJSONFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := writeJSONFile(path, map[string]string{"a": "b"}); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	// 不能留下临时文件。
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("不该留下 .tmp 文件")
	}
	var out map[string]string
	if err := readJSONFile(path, &out); err != nil || out["a"] != "b" {
		t.Fatalf("往返失败: %v %v", out, err)
	}
}

func TestReadJSONFileMissing(t *testing.T) {
	var out map[string]string
	if err := readJSONFile(filepath.Join(t.TempDir(), "nope.json"), &out); err == nil {
		t.Fatal("文件不存在时应返回错误")
	}
	if out != nil {
		t.Fatalf("出错时不应污染 out: %v", out)
	}
}
