package admin

import (
	"strings"
	"testing"
)

func TestCPUUsageFromDelta(t *testing.T) {
	// 100 total jiffies, 25 idle → 75% used
	got := cpuUsageFromDelta(0, 0, 25, 100)
	if got != 75 {
		t.Fatalf("cpuUsageFromDelta() = %v, want 75", got)
	}
	// No progress
	if cpuUsageFromDelta(10, 100, 10, 100) != 0 {
		t.Fatalf("zero delta should be 0")
	}
	// Fully idle
	if cpuUsageFromDelta(0, 0, 100, 100) != 0 {
		t.Fatalf("full idle should be 0")
	}
}

func TestReadResourcesSanity(t *testing.T) {
	res := readResources()
	if res.CPUPercent < 0 || res.CPUPercent > 100 {
		t.Fatalf("CPUPercent = %v", res.CPUPercent)
	}
	if res.MemoryPercent < 0 || res.MemoryPercent > 100 {
		t.Fatalf("MemoryPercent = %v", res.MemoryPercent)
	}
	if res.DiskPercent < 0 || res.DiskPercent > 100 {
		t.Fatalf("DiskPercent = %v", res.DiskPercent)
	}
	// Second call should still be sane (uses previous sample, no forced sleep path after first).
	res2 := readResources()
	if res2.CPUPercent < 0 || res2.CPUPercent > 100 {
		t.Fatalf("CPUPercent2 = %v", res2.CPUPercent)
	}
}

func TestParseActivityFiltersNoiseAndOrdersNewestFirst(t *testing.T) {
	lines := []string{
		"2026/07/16 12:00:00 [NIM-room] active live discovery",
		"2026/07/16 12:00:01 [NIM-room] QChat connected",
		"2026/07/16 12:00:02 [Douyin-IM] status=connected",
	}
	items := parseActivity(lines, 10)
	if len(items) != 2 {
		t.Fatalf("parseActivity() returned %d items, want 2", len(items))
	}
	if items[0].Time != "12:00:02" || items[0].Source != "Douyin-IM" {
		t.Fatalf("newest item = %#v", items[0])
	}
	if items[1].Level != "success" {
		t.Fatalf("connected item level = %q, want success", items[1].Level)
	}
}

func TestCleanLogMessageTruncatesLongContent(t *testing.T) {
	message := cleanLogMessage("2026/07/16 12:00:00 [QChat] " + string(make([]byte, 200)))
	if len([]rune(message)) > 121 {
		t.Fatalf("message length = %d, want at most 121", len([]rune(message)))
	}
}

func TestDouyinInitMissingIsConnectingNotLoginRequired(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/16 12:00:02 [Douyin-IM] status=init_missing message=retrying",
	})
	for _, state := range states {
		if state.ID != "douyin_im" {
			continue
		}
		if state.StatusText != "未连接" || state.LastEvent != "新版网页未提供 IM 初始化接口" {
			t.Fatalf("Douyin state = %#v", state)
		}
		return
	}
	t.Fatal("Douyin state not found")
}

func TestDouyinReadyIsHealthy(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/17 11:01:45 [Douyin] status=ready message=抖音作品监控已就绪",
	})
	for _, state := range states {
		if state.ID != "douyin" {
			continue
		}
		if state.Status != "healthy" || state.StatusText != "运行中" || state.LastTime != "11:01:45" {
			t.Fatalf("Douyin state = %#v", state)
		}
		return
	}
	t.Fatal("Douyin state not found")
}

func TestDouyinLoginErrorIsDown(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/17 11:01:45 [Douyin] status=login_error message=cookies failed",
	})
	for _, state := range states {
		if state.ID == "douyin" && state.Status != "down" {
			t.Fatalf("Douyin state = %#v", state)
		}
	}
}

func TestDouyinWorksScanCookieYesIsHealthy(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/19 15:20:43 [Weibo-auth] douyin works scan via HTTP accounts=4 cookie=yes",
	})
	for _, state := range states {
		if state.ID != "douyin" {
			continue
		}
		if state.Status != "healthy" || state.StatusText != "运行中" || state.LastTime != "15:20:43" {
			t.Fatalf("Douyin state = %#v", state)
		}
		if !strings.Contains(state.LastEvent, "作品监控") {
			t.Fatalf("event = %q", state.LastEvent)
		}
		return
	}
	t.Fatal("Douyin state not found")
}

func TestDouyinWorksScanCookieNoNeedsLogin(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/19 15:20:43 [Weibo-auth:stdout] [weibo-auth] douyin works scan via HTTP accounts=1 cookie=no",
	})
	for _, state := range states {
		if state.ID != "douyin" {
			continue
		}
		if state.Status != "attention" || state.StatusText != "待登录" {
			t.Fatalf("Douyin state = %#v", state)
		}
		return
	}
	t.Fatal("Douyin state not found")
}

func TestDouyinLoginRequiredBeatsOlderWorksScan(t *testing.T) {
	// bottom-up: newer login_required must win over older cookie=yes
	states := buildServiceStates([]string{
		"2026/07/19 15:00:00 [Weibo-auth] douyin works scan via HTTP accounts=4 cookie=yes",
		"2026/07/19 15:10:00 [Douyin] status=login_required message=抖音浏览器需要登录",
	})
	for _, state := range states {
		if state.ID != "douyin" {
			continue
		}
		if state.Status != "attention" || state.StatusText != "待登录" {
			t.Fatalf("Douyin state = %#v", state)
		}
		return
	}
	t.Fatal("Douyin state not found")
}


func TestNapCatFailedToConnectIsDown(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/18 10:01:12 ❌ Failed to connect to NapCat: dial tcp 127.0.0.1:3001: connect: connection refused. Retrying in 5s...",
	})
	for _, state := range states {
		if state.ID != "napcat" {
			continue
		}
		if state.Status != "down" || state.StatusText != "连接中断" {
			t.Fatalf("napcat state = %#v", state)
		}
		return
	}
	t.Fatal("napcat state not found")
}

func TestNapCatStatusDisconnectedIsDown(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/18 10:00:00 ✅ Connected to NapCat successfully",
		"2026/07/18 10:05:00 [NapCat] status=disconnected message=read loop ended",
	})
	for _, state := range states {
		if state.ID != "napcat" {
			continue
		}
		if state.Status != "down" {
			t.Fatalf("napcat state = %#v", state)
		}
		return
	}
	t.Fatal("napcat state not found")
}

func TestNIMHealthPopulatesQChatAndLiveStates(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/17 09:00:00 [NIM-health] qchat=connected",
		"2026/07/17 09:00:00 [NIM-live-health] status=idle connected=0 configured=0",
	})
	statusByID := make(map[string]serviceState)
	for _, state := range states {
		statusByID[state.ID] = state
	}
	if statusByID["qchat"].Status != "healthy" {
		t.Fatalf("qchat state = %#v", statusByID["qchat"])
	}
	if statusByID["pocket_live"].StatusText != "待命" {
		t.Fatalf("live state = %#v", statusByID["pocket_live"])
	}
}

func TestNewestLiveDiscoveryFailureOverridesOlderHealthyStatus(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/17 09:00:00 [NIM-live-health] status=idle connected=0 configured=0",
		"2026/07/17 09:00:30 [NIM-live] active live discovery failed: bad response",
	})
	for _, state := range states {
		if state.ID == "pocket_live" && state.Status != "down" {
			t.Fatalf("live state = %#v", state)
		}
	}
}

func TestNapCatDisconnectOverridesOlderConnection(t *testing.T) {
	states := buildServiceStates([]string{
		"2026/07/17 09:00:00 ✅ Connected to NapCat successfully",
		"2026/07/17 09:00:30 ⚠️ NapCat read error (disconnected?): unexpected EOF",
	})
	for _, state := range states {
		if state.ID == "napcat" {
			if state.Status != "down" || state.StatusText != "连接中断" {
				t.Fatalf("NapCat state = %#v", state)
			}
			return
		}
	}
	t.Fatal("NapCat state not found")
}
