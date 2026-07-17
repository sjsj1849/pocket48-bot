package admin

import "testing"

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
