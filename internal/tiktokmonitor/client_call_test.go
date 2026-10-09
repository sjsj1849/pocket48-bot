package tiktokmonitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeCollector 造一个「行为像 sidecar」的脚本：
//   - 先输出结果行
//   - 然后 sleep hangSec 秒再退出（模拟浏览器收尾挂死）
func fakeCollector(t *testing.T, dir, body string, hangSec int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("需要 POSIX shell")
	}
	path := filepath.Join(dir, "fake_sidecar.py")
	script := fmt.Sprintf(`import sys, time
sys.stdin.read()
print(%q)
sys.stdout.flush()
time.sleep(%d)
`, body, hangSec)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写 fake sidecar 失败: %v", err)
	}
	return path
}

// TestCallReturnsBeforeSidecarExits 是本次线上故障的回归测试。
//
// 背景（2026-10-04）：sidecar 拿到数据、打完 stdout 之后要做浏览器收尾，
// 而收尾随时可能挂死。原来的 Call 用 exec.CommandContext + Run()，
// Run() 会一直等到**进程退出**，于是一次 5.7 秒就能拿到的采集，
// 被硬生生拖到 120 秒超时 —— 而且日志里最后一行永远停在「打开主页」，
// 看起来像 TikTok 慢，越查越偏。
//
// 现在 Call 读到能解析成 envelope 的那一行就立刻返回，
// 所以「sidecar 挂死」不应该影响调用方拿到结果。
func TestCallReturnsBeforeSidecarExits(t *testing.T) {
	dir := t.TempDir()
	body := `{"ok":true,"data":{"user":{"username":"h2h","userId":"1"},"videos":[{"id":"v1","desc":"t","createTime":100,"duration":116.0,"authorId":"1","authorName":"h2h","stats":{"play":1,"like":2,"comment":3,"share":4}}]}}`
	// sidecar 打完结果后挂死 60 秒，测试只给 20 秒超时。
	fakeCollector(t, dir, body, 60)

	c := Client{Dir: filepath.Join(dir, "storage", "tiktok")}
	// 把脚本路径指到我们造的那个假 sidecar。
	clientForScript := clientWithScript{Client: c, script: filepath.Join(dir, "fake_sidecar.py")}

	ctx := context.Background()
	start := time.Now()
	resp, err := clientForScript.call(ctx, map[string]any{"operation": "timeline"}, 20*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("应在 sidecar 挂死时仍拿到结果，实际 %s 后失败: %v",
			elapsed.Round(time.Millisecond), err)
	}
	if len(resp.Videos) != 1 {
		t.Fatalf("应返回 1 条作品，实际 %d 条", len(resp.Videos))
	}
	if resp.Videos[0].ID != "v1" {
		t.Fatalf("作品 ID 应为 v1，实际 %q", resp.Videos[0].ID)
	}
	// sidecar 挂着 60 秒，这里必须远小于它，否则说明又在等进程退出。
	if elapsed > 15*time.Second {
		t.Fatalf("返回耗时 %s，明显在等 sidecar 退出（应读到结果即返回）",
			elapsed.Round(time.Millisecond))
	}
	t.Logf("★ sidecar 挂死 60s，Call %s 即返回结果", elapsed.Round(time.Millisecond))
}

// TestCallSurfacesSidecarError 确认错误响应照样能透传，别被新的读法吃掉。
func TestCallSurfacesSidecarError(t *testing.T) {
	dir := t.TempDir()
	body := `{"ok":false,"error":{"code":"rate_limited","message":"接口连续返回空响应"}}`
	fakeCollector(t, dir, body, 0)

	c := Client{Dir: filepath.Join(dir, "storage", "tiktok")}
	clientForScript := clientWithScript{Client: c, script: filepath.Join(dir, "fake_sidecar.py")}

	_, err := clientForScript.call(context.Background(), map[string]any{"operation": "timeline"}, 20*time.Second)
	if err == nil {
		t.Fatal("应返回错误")
	}
	// 未知码也要带出 message —— 线上排查全靠这句原文。
	if !strings.Contains(err.Error(), "rate_limited") ||
		!strings.Contains(err.Error(), "接口连续返回空响应") {
		t.Fatalf("错误应带出 code 和 message，实际: %v", err)
	}
}

// TestReadEnvelopeSkipsNonJSONLines 确认 stdout 里的杂音行会被跳过。
//
// sidecar 协议规定 stdout 只有 JSON，但一旦有人误把日志写进 stdout
// （比如改了 log() 的输出流），整次调用不该因此失败。
func TestReadEnvelopeSkipsNonJSONLines(t *testing.T) {
	input := strings.NewReader(
		"[tiktok-monitor] 启动 Chromium\n" +
			"[tiktok-monitor] 打开 https://www.tiktok.com/@h2h\n" +
			`{"ok":true,"data":{"videos":[]}}` + "\n")
	payload, overLimit, err := readEnvelope(input)
	if err != nil {
		t.Fatalf("应跳过杂音行读到结果，实际报错: %v", err)
	}
	if overLimit {
		t.Fatal("不该超限")
	}
	if !payload.OK {
		t.Fatal("应解析为成功响应")
	}
}

// TestReadEnvelopeTimesOut 无有效 JSON 时要报错，而不是死等。
func TestReadEnvelopeTimesOut(t *testing.T) {
	// 只有一行不构成协议响应的 JSON，且没有 ok 字段。
	input := strings.NewReader(`{"data":"something"}`)
	if _, _, err := readEnvelope(input); err == nil {
		t.Fatal("没有 ok 字段时应返回错误")
	}
}

// clientWithScript 允许测试覆盖 sidecar 脚本路径。
type clientWithScript struct {
	Client
	script string
}

func (c clientWithScript) call(ctx context.Context, req map[string]any, timeout time.Duration) (*response, error) {
	return callScript(ctx, c.Client, c.script, req, timeout)
}

// 调用后确认 JSON 结构与 sidecar 的 envelope 定义一致。
func TestEnvelopeFieldNames(t *testing.T) {
	raw := []byte(`{"ok":true,"data":{"user":{"username":"h2h","userId":"1","nickname":"Hearts2Hearts"},"videos":[{"id":"1","desc":"d","createTime":1,"duration":2.5,"authorId":"a","authorName":"n","authorNickname":"N","stats":{"play":1,"like":2,"comment":3,"share":4},"musicTitle":"m","cover":"c","width":720,"height":1280}]}}`)
	var p envelopePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("envelope 结构应能解析: %v", err)
	}
	if !p.OK || len(p.Data.Videos) != 1 {
		t.Fatalf("解析结果不对: %+v", p)
	}
	if p.Data.Videos[0].AuthorNick != "N" || p.Data.Videos[0].Stats.Share != 4 {
		t.Fatalf("字段映射不对: %+v", p.Data.Videos[0])
	}
}
