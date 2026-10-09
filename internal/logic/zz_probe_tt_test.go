package logic

import (
	"testing"

	"pocket48-bot/internal/tiktokmonitor"
)

// Temporary diagnostic for TestPushesInChronologicalOrder.
func TestZZProbeTiktokSeconds(t *testing.T) {
	v := tiktokmonitor.Video{ID: "1", Duration: 10, Desc: "old", AuthorName: "hearts2hearts"}
	t.Logf("Seconds(10) = %d", tiktokmonitor.Seconds(v))

	v2 := tiktokmonitor.Video{ID: "2", Duration: 10.5, Desc: "x"}
	t.Logf("Seconds(10.5) = %d", tiktokmonitor.Seconds(v2))

	v3 := tiktokmonitor.Video{ID: "3", Desc: "no duration"}
	t.Logf("Seconds(0) = %d", tiktokmonitor.Seconds(v3))

	t.Logf("DefaultUser = %q", tiktokmonitor.DefaultUser)
}
