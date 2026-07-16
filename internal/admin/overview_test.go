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
