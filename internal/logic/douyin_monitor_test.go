package logic

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
)

func TestUnseenDouyinPosts(t *testing.T) {
	now := time.Unix(1_000, 0)
	posts := []douyinPost{
		{ID: "pinned-old", CreateTime: 100},
		{ID: "3", CreateTime: 900},
		{ID: "2", CreateTime: 800},
		{ID: "future", CreateTime: 2_000},
	}
	got := unseenDouyinPosts(posts, 700, now)
	if len(got) != 2 || got[0].ID != "2" || got[1].ID != "3" {
		t.Fatalf("unexpected posts: %#v", got)
	}
	latest, ok := latestTimestampedDouyinPost(posts, now)
	if !ok || latest.ID != "3" {
		t.Fatalf("latest timestamped post: %#v, ok=%v", latest, ok)
	}
}

func TestCanonicalDouyinPostURL(t *testing.T) {
	if got := canonicalDouyinPostURL(douyinPost{ID: "123", Type: "note", URL: "https://example.com/long"}); got != "https://www.douyin.com/note/123" {
		t.Fatalf("note URL=%q", got)
	}
	if got := canonicalDouyinPostURL(douyinPost{ID: "456", Type: "video"}); got != "https://www.douyin.com/video/456" {
		t.Fatalf("video URL=%q", got)
	}
}

func TestExtractDouyinOnline(t *testing.T) {
	body := map[string]interface{}{"payload": map[string]interface{}{"onlineUserForAnchor": "321"}}
	if got := extractDouyinOnline(body); got != 321 {
		t.Fatalf("online=%d", got)
	}
}

func TestDouyinStatsUseOnlyExplicitPaths(t *testing.T) {
	body := map[string]interface{}{
		"payload": map[string]interface{}{
			"onlineUserCount": float64(50),
			"audienceCount":   float64(100),
			"unrelated": map[string]interface{}{
				"userCount":     float64(9999),
				"audienceCount": float64(8888),
			},
		},
	}
	if got := extractDouyinCurrentOnline(body); got != 50 {
		t.Fatalf("current online=%d", got)
	}
	if got := extractDouyinTotalAudience(body); got != 100 {
		t.Fatalf("total audience=%d", got)
	}
	onlyUnrelated := map[string]interface{}{"payload": map[string]interface{}{"unrelated": map[string]interface{}{"userCount": float64(9999)}}}
	if got := extractDouyinCurrentOnline(onlyUnrelated); got != 0 {
		t.Fatalf("recursive unrelated userCount was accepted: %d", got)
	}
}

func TestExtractDouyinRoomIDUsesExplicitPaths(t *testing.T) {
	body := map[string]interface{}{"payload": map[string]interface{}{"room": map[string]interface{}{"id": "real-room-id"}}}
	if got := extractDouyinRoomID(body); got != "real-room-id" {
		t.Fatalf("room id=%q", got)
	}
}

func newDouyinLiveTestMonitor(t *testing.T) *DouyinMonitor {
	t.Helper()
	return &DouyinMonitor{
		cfg:             &config.Config{DouyinLiveSummaryEnabled: true, DouyinLiveSoundWaveEnabled: true, DouyinSubscriptions: map[int64]map[string]*config.DouyinConfig{}},
		liveStates:      make(map[string]*douyinLiveState),
		liveCancels:     make(map[string]context.CancelFunc),
		livePersistedAt: make(map[string]time.Time),
		liveSessionDir:  t.TempDir(),
		enqueueGroup:    func(int64, interface{}) bool { return true },
	}
}

func addDouyinLiveTestTargets(m *DouyinMonitor, liveID string, groups ...int64) {
	for _, groupID := range groups {
		if m.cfg.DouyinSubscriptions[groupID] == nil {
			m.cfg.DouyinSubscriptions[groupID] = make(map[string]*config.DouyinConfig)
		}
		m.cfg.DouyinSubscriptions[groupID]["creator"] = &config.DouyinConfig{SecUserID: "creator", Name: "主播", LiveID: liveID, AtAll: groupID%2 == 0}
	}
}

func TestDouyinOnlineAndEndedOnlyOnce(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "room-1", 100)
	sends := 0
	var queuedMessages []interface{}
	m.enqueueGroup = func(_ int64, message interface{}) bool {
		sends++
		queuedMessages = append(queuedMessages, message)
		return true
	}
	m.liveOnline("room-1", "actual-room-1", "主播", "标题")
	first := m.liveStates["room-1"].DetectedStartedAt
	m.liveOnline("room-1", "actual-room-1", "主播", "重复")
	if !m.liveStates["room-1"].DetectedStartedAt.Equal(first) || !m.liveStates["room-1"].StartNotificationSent {
		t.Fatal("duplicate ROOM_ONLINE reset the session")
	}
	if sends != 1 {
		t.Fatalf("duplicate ROOM_ONLINE sends=%d", sends)
	}
	segments, ok := queuedMessages[0].([]interface{})
	if !ok || len(segments) < 2 || segments[0].(napcat.MessageSegment).Type != "at" {
		t.Fatalf("AtAll message=%#v", queuedMessages[0])
	}
	textSegment, ok := segments[1].(napcat.MessageSegment)
	if !ok || !strings.HasPrefix(textSegment.Data["text"], "\n【") {
		t.Fatalf("AtAll title is not on its own line: %#v", queuedMessages[0])
	}
	m.liveEnded("room-1", "actual-room-1", "", "")
	updated := m.liveStates["room-1"].LastUpdatedAt
	m.liveEnded("room-1", "actual-room-1", "", "")
	if !m.liveStates["room-1"].LastUpdatedAt.Equal(updated) || !m.liveStates["room-1"].EndNotificationSent {
		t.Fatal("duplicate ROOM_ENDED changed the completed session")
	}
	if sends != 2 {
		t.Fatalf("duplicate ROOM_ENDED sends=%d", sends)
	}
}

func TestDouyinRealtimeStatsAreIgnored(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "room-1", 100)
	m.liveOnline("room-1", "actual-room-1", "主播", "标题")
	m.handleLiveMessage("room-1", []byte(`{"method":"WebcastRoomStatsMessage","payload":{"onlineUserCount":50,"audienceCount":100,"total":999999}}`))
	m.handleLiveMessage("room-1", []byte(`{"method":"WebcastRoomStatsMessage","payload":{"onlineUserCount":20,"audienceCount":80}}`))
	state := m.liveStates["room-1"]
	if state.CurrentOnline != 0 || state.PeakOnline != 0 || state.TotalAudience != 0 {
		t.Fatalf("realtime statistics should be ignored: %#v", state)
	}
	if got := extractDouyinCurrentOnline(map[string]interface{}{"total": float64(123)}); got != 0 {
		t.Fatalf("generic total used as current online: %d", got)
	}
}

func giftBody(msgID string, repeat int64, repeatEnd bool, diamond interface{}) map[string]interface{} {
	gift := map[string]interface{}{"id": "gift-1", "name": "礼物"}
	if diamond != nil {
		gift["diamondCount"] = diamond
	}
	return map[string]interface{}{
		"method": "WebcastGiftMessage",
		"payload": map[string]interface{}{
			"gift": gift, "common": map[string]interface{}{"msgId": msgID},
			"user": map[string]interface{}{"id": "user-1"}, "comboId": "combo-1",
			"repeatCount": repeat, "repeatEnd": repeatEnd,
		},
	}
}

func TestDouyinGiftComboDedupAndMissingPrice(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.liveOnline("room-1", "actual-room-1", "主播", "标题")
	m.applyDouyinGift("room-1", giftBody("m1", 1, false, float64(10)))
	m.applyDouyinGift("room-1", giftBody("m2", 3, false, float64(10)))
	m.applyDouyinGift("room-1", giftBody("m2", 3, false, float64(10)))
	state := m.liveStates["room-1"]
	if state.EstimatedSoundWave != 30 || state.GiftEventCount != 2 {
		t.Fatalf("sound=%d events=%d", state.EstimatedSoundWave, state.GiftEventCount)
	}
	m.applyDouyinGift("room-1", giftBody("m3", 4, true, float64(10)))
	if len(state.ComboGiftCounts) != 0 || state.EstimatedSoundWave != 40 {
		t.Fatalf("repeatEnd state=%#v sound=%d", state.ComboGiftCounts, state.EstimatedSoundWave)
	}
	m.applyDouyinGift("room-1", giftBody("m4", 1, false, nil))
	if state.EstimatedSoundWave != 40 || state.GiftEventCount != 4 {
		t.Fatalf("missing diamondCount fabricated value: sound=%d events=%d", state.EstimatedSoundWave, state.GiftEventCount)
	}
}

func TestDouyinOrdinaryGiftUsesDiamondCount(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.liveOnline("room-1", "actual-room-1", "主播", "标题")
	body := map[string]interface{}{
		"gift":   map[string]interface{}{"id": "gift-1", "diamondCount": float64(25)},
		"common": map[string]interface{}{"msgId": "ordinary-1"},
		"user":   map[string]interface{}{"id": "user-1"},
	}
	m.applyDouyinGift("room-1", body)
	if got := m.liveStates["room-1"].EstimatedSoundWave; got != 25 {
		t.Fatalf("ordinary gift sound=%d", got)
	}
}

func TestDouyinLiveStatePersistsAndSuppressesRestartDuplicates(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "room-restore", 100)
	m.liveOnline("room-restore", "actual-room-restore", "主播", "标题")
	m.liveStates["room-restore"].PeakOnline = 77
	m.applyDouyinGift("room-restore", giftBody("persisted-message", 2, false, float64(10)))
	m.persistLiveState("room-restore", true)
	restored := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(restored, "room-restore", 100)
	restored.liveSessionDir = m.liveSessionDir
	restored.loadLiveStatesFromDisk()
	state := restored.liveStates["room-restore"]
	if state == nil || !state.Online || !state.StartNotificationSent || state.PeakOnline != 77 || state.EstimatedSoundWave != 20 || len(state.ProcessedGiftMessageIDs) != 1 || len(state.ComboGiftCounts) != 1 {
		t.Fatalf("restored state=%#v", state)
	}
	started := state.DetectedStartedAt
	restored.liveOnline("room-restore", "actual-room-restore", "主播", "重复")
	if !state.DetectedStartedAt.Equal(started) {
		t.Fatal("restart caused duplicate online transition")
	}
	restored.liveEnded("room-restore", "actual-room-restore", "", "")
	completed := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(completed, "room-restore", 100)
	completed.liveSessionDir = m.liveSessionDir
	completed.loadLiveStatesFromDisk()
	endedAt := completed.liveStates["room-restore"].LastUpdatedAt
	completed.liveEnded("room-restore", "actual-room-restore", "", "")
	if !completed.liveStates["room-restore"].LastUpdatedAt.Equal(endedAt) {
		t.Fatal("restart caused duplicate end transition")
	}
}

func TestDouyinSameLiveIDCreatesNextSession(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "reused-live", 100)
	sends := 0
	m.enqueueGroup = func(int64, interface{}) bool { sends++; return true }
	m.liveOnline("reused-live", "room-a", "主播", "第一场")
	first := m.liveStates["reused-live"]
	firstSessionID := first.SessionID
	first.PeakOnline = 88
	first.EstimatedSoundWave = 100
	first.ProcessedGiftMessageIDs = []string{"old-message"}
	m.liveEnded("reused-live", "room-a", "", "")
	m.liveOnline("reused-live", "room-b", "主播", "第二场")
	second := m.liveStates["reused-live"]
	if second.SessionID == firstSessionID || second.RoomID != "room-b" {
		t.Fatalf("next session not created: first=%s second=%#v", firstSessionID, second)
	}
	if second.PeakOnline != 0 || second.EstimatedSoundWave != 0 || len(second.ProcessedGiftMessageIDs) != 0 {
		t.Fatalf("next session inherited previous statistics: %#v", second)
	}
	if sends != 3 { // first start, first end, second start
		t.Fatalf("notification sends=%d", sends)
	}
	entries, err := os.ReadDir(m.liveSessionDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("session history entries=%d err=%v", len(entries), err)
	}
}

func TestDouyinChangedRealRoomIDClosesMissedSession(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "stable-live", 100)
	sends := 0
	m.enqueueGroup = func(int64, interface{}) bool { sends++; return true }
	m.liveOnline("stable-live", "room-a", "主播", "第一场")
	firstSessionID := m.liveStates["stable-live"].SessionID
	// No ROOM_ENDED: a different upstream room ID still defines a new session.
	m.liveOnline("stable-live", "room-b", "主播", "第二场")
	state := m.liveStates["stable-live"]
	if state.SessionID == firstSessionID || state.RoomID != "room-b" || sends != 3 {
		t.Fatalf("room boundary state=%#v sends=%d", state, sends)
	}
}

func TestDouyinPendingNotificationRetriesAfterRestart(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "pending-live", 100, 200)
	m.enqueueGroup = func(int64, interface{}) bool { return false }
	m.liveOnline("pending-live", "room-a", "主播", "标题")
	if state := m.liveStates["pending-live"]; !state.StartNotificationPending || state.StartNotificationSent {
		t.Fatalf("pending state not persisted: %#v", state)
	}

	restored := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(restored, "pending-live", 100, 200)
	restored.liveSessionDir = m.liveSessionDir
	restored.loadLiveStatesFromDisk()
	sends := map[int64]int{}
	restored.enqueueGroup = func(groupID int64, _ interface{}) bool { sends[groupID]++; return true }
	restored.retryPendingLiveNotifications()
	state := restored.liveStates["pending-live"]
	if sends[100] != 1 || sends[200] != 1 || state.StartNotificationPending || !state.StartNotificationSent {
		t.Fatalf("retry sends=%#v state=%#v", sends, state)
	}
}

func TestDouyinPartialMultiGroupQueueResumesOnlyMissingGroup(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "partial-live", 100, 200)
	m.enqueueGroup = func(groupID int64, _ interface{}) bool { return groupID == 100 }
	m.liveOnline("partial-live", "room-a", "主播", "标题")
	state := m.liveStates["partial-live"]
	if !state.StartNotificationQueued["100|creator"] || state.StartNotificationQueued["200|creator"] || !state.StartNotificationPending {
		t.Fatalf("partial queue state=%#v", state)
	}

	restored := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(restored, "partial-live", 100, 200)
	restored.liveSessionDir = m.liveSessionDir
	restored.loadLiveStatesFromDisk()
	sends := map[int64]int{}
	restored.enqueueGroup = func(groupID int64, _ interface{}) bool { sends[groupID]++; return true }
	restored.retryPendingLiveNotifications()
	if sends[100] != 0 || sends[200] != 1 || !restored.liveStates["partial-live"].StartNotificationSent {
		t.Fatalf("resume sends=%#v state=%#v", sends, restored.liveStates["partial-live"])
	}
}

func TestDouyinPendingEndNotificationRetriesAfterRestart(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(m, "pending-end", 100)
	m.liveOnline("pending-end", "room-a", "主播", "标题")
	m.enqueueGroup = func(int64, interface{}) bool { return false }
	m.liveEnded("pending-end", "room-a", "", "")
	if state := m.liveStates["pending-end"]; !state.EndNotificationPending || state.EndNotificationSent {
		t.Fatalf("end pending state=%#v", state)
	}

	restored := newDouyinLiveTestMonitor(t)
	addDouyinLiveTestTargets(restored, "pending-end", 100)
	restored.liveSessionDir = m.liveSessionDir
	restored.loadLiveStatesFromDisk()
	sends := 0
	restored.enqueueGroup = func(int64, interface{}) bool { sends++; return true }
	restored.retryPendingLiveNotifications()
	state := restored.liveStates["pending-end"]
	if sends != 1 || state.EndNotificationPending || !state.EndNotificationSent {
		t.Fatalf("end retry sends=%d state=%#v", sends, state)
	}
}

func TestDouyinSummaryOmitsUnavailableFields(t *testing.T) {
	got := appendDouyinLiveSummary("直播已结束", time.Hour+2*time.Minute+3*time.Second, 32480, 0, 0, false)
	if !strings.Contains(got, "监测时长：1小时2分3秒") || !strings.Contains(got, "最高在线：32,480") {
		t.Fatalf("summary=%q", got)
	}
	if strings.Contains(got, "累计场观") || strings.Contains(got, "音浪") || strings.Contains(got, "未知") {
		t.Fatalf("unavailable fields leaked into summary=%q", got)
	}
}

func TestDisabledDouyinSubscriptionDoesNotCreateAccountTask(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.cfg.DouyinSubscriptions[1] = map[string]*config.DouyinConfig{
		"enabled":  {SecUserID: "enabled", LiveID: "live-1"},
		"disabled": {SecUserID: "disabled", LiveID: "live-2", Disabled: true},
	}
	accounts := m.accountsLocked()
	if len(accounts) != 1 || accounts[0].SecUserID != "enabled" {
		t.Fatalf("accounts=%#v", accounts)
	}
}

func TestDouyinWorksAndLiveTogglesRouteIndependently(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.cfg.DouyinSubscriptions = map[int64]map[string]*config.DouyinConfig{
		1: {
			"works": {SecUserID: "works", LiveID: "works-live", LiveDisabled: true},
			"live":  {SecUserID: "live", LiveID: "live-only", WorksDisabled: true},
			"off":   {SecUserID: "off", LiveID: "off-live", WorksDisabled: true, LiveDisabled: true},
		},
	}
	accounts := m.accountsLocked()
	if len(accounts) != 2 {
		t.Fatalf("browser accounts=%#v", accounts)
	}
	accountFlags := make(map[string]douyinAccountCommand, len(accounts))
	for _, account := range accounts {
		accountFlags[account.SecUserID] = account
	}
	if !accountFlags["works"].WorksEnabled || accountFlags["works"].LiveEnabled {
		t.Fatalf("works-only flags=%#v", accountFlags["works"])
	}
	if accountFlags["live"].WorksEnabled || !accountFlags["live"].LiveEnabled {
		t.Fatalf("live-only flags=%#v", accountFlags["live"])
	}
	if bridgeAccounts := douyinAccountsFromConfig(m.cfg); len(bridgeAccounts) != 2 || !bridgeAccounts[1].WorksEnabled && !bridgeAccounts[1].LiveEnabled {
		t.Fatalf("bridge accounts=%#v", bridgeAccounts)
	}
	desired := m.desiredLiveIDsLocked()
	if len(desired) != 1 || !desired["live-only"] {
		t.Fatalf("desired live IDs=%#v", desired)
	}
	m.handlePosts(douyinBrowserEvent{SecUserID: "live", Posts: []douyinPost{{ID: "post", CreateTime: time.Now().Unix()}}})
	if got := m.cfg.DouyinSubscriptions[1]["live"].LastAwemeTime; got != 0 {
		t.Fatalf("works-disabled subscription advanced cursor: %d", got)
	}
}

func TestDouyinManualNicknameIsNotOverwritten(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.cfg.DouyinSubscriptions = map[int64]map[string]*config.DouyinConfig{
		1: {"creator": {SecUserID: "creator", Name: "面板昵称", NameManual: true}},
	}
	m.handleAccount(douyinBrowserEvent{SecUserID: "creator", Nickname: "平台昵称"})
	if got := m.cfg.DouyinSubscriptions[1]["creator"].Name; got != "面板昵称" {
		t.Fatalf("manual nickname overwritten: %q", got)
	}
}

func TestDouyinLiveTargetsPreserveGroupsAndAtAll(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.cfg.DouyinSubscriptions = map[int64]map[string]*config.DouyinConfig{
		100: {"creator": {SecUserID: "creator", LiveID: "live", AtAll: true}},
		200: {"creator": {SecUserID: "creator", LiveID: "live", AtAll: false}},
		300: {"creator": {SecUserID: "creator", LiveID: "live", Disabled: true}},
	}
	targets := m.liveTargets("live")
	if len(targets) != 2 {
		t.Fatalf("targets=%#v", targets)
	}
	seen := map[int64]bool{}
	for _, target := range targets {
		seen[target.groupID] = target.cfg.AtAll
	}
	if !seen[100] || seen[200] {
		t.Fatalf("AtAll/group relationship lost: %#v", seen)
	}
}

func TestDouyinSoundWaveDisabledSkipsGiftState(t *testing.T) {
	m := newDouyinLiveTestMonitor(t)
	m.cfg.DouyinLiveSoundWaveEnabled = false
	m.liveOnline("room-1", "actual-room-1", "主播", "标题")
	raw := []byte(`{"method":"WebcastGiftMessage","gift":{"id":"gift","diamondCount":10},"common":{"msgId":"m1"},"user":{"id":"u1"}}`)
	m.handleLiveMessage("room-1", raw)
	state := m.liveStates["room-1"]
	if state.GiftEventCount != 0 || state.EstimatedSoundWave != 0 || len(state.ProcessedGiftMessageIDs) != 0 {
		t.Fatalf("disabled gift monitoring mutated state: %#v", state)
	}
}

func TestResolveDouyinTargetSecUserID(t *testing.T) {
	sec, profile, err := ResolveDouyinTarget(context.Background(), "MS4wLjABAAAA_test-user")
	if err != nil || sec != "MS4wLjABAAAA_test-user" || profile == "" {
		t.Fatalf("resolve=(%q,%q,%v)", sec, profile, err)
	}
}

func TestResolveDouyinTargetDirectProfileDoesNotNeedNetwork(t *testing.T) {
	sec, _, err := ResolveDouyinTarget(context.Background(), "https://www.douyin.com/user/MS4wLjABAAAA_direct")
	if err != nil || sec != "MS4wLjABAAAA_direct" {
		t.Fatalf("resolve direct=(%q,%v)", sec, err)
	}
	if _, _, err := ResolveDouyinTarget(context.Background(), "https://example.com/user/MS4wLjABAAAA_direct"); err == nil {
		t.Fatal("non-Douyin URL should be rejected")
	}
}

func TestParseDouyinCommandLine(t *testing.T) {
	got, err := parseDouyinCommandLine(`node "./sidecar/path with space/index.mjs"`, "")
	if err != nil || len(got) != 2 || got[1] != "./sidecar/path with space/index.mjs" {
		t.Fatalf("parse=%#v err=%v", got, err)
	}
}

func TestClassifyDouyinIMEvent(t *testing.T) {
	group := douyinBrowserEvent{ConversationType: 2, ConversationID: "target", SenderUID: "owner"}
	if got := classifyDouyinIMEvent(group, "target", "owner", "self"); got != "group_owner" {
		t.Fatalf("group owner classification=%q", got)
	}
	group.SenderUID = "member"
	if got := classifyDouyinIMEvent(group, "target", "owner", "self"); got != "" {
		t.Fatalf("ordinary group member must be ignored, got %q", got)
	}
	private := douyinBrowserEvent{ConversationType: 1, SenderUID: "peer"}
	if got := classifyDouyinIMEvent(private, "target", "owner", "self"); got != "private_incoming" {
		t.Fatalf("incoming private classification=%q", got)
	}
	private.SenderUID = "self"
	if got := classifyDouyinIMEvent(private, "target", "owner", "self"); got != "" {
		t.Fatalf("outgoing private message must be ignored, got %q", got)
	}
}

func TestFormatDouyinIMTime(t *testing.T) {
	if got := formatDouyinIMTime(1784251775, 0); got != "2026-07-17 09:29:35" {
		t.Fatalf("seconds timestamp=%q", got)
	}
	if got := formatDouyinIMTime(0, 1784251775000); got != "2026-07-17 09:29:35" {
		t.Fatalf("millisecond fallback=%q", got)
	}
}

func TestFormatDouyinIMNotification(t *testing.T) {
	got := formatDouyinIMNotification("【肥家|抖音群】", "一盆蘸酱菜", "消息正文", "2026-07-17 10:00:00")
	want := "【肥家|抖音群】\n一盆蘸酱菜: 消息正文\n2026-07-17 10:00:00"
	if got != want {
		t.Fatalf("notification=%q", got)
	}
}

func TestFormatDouyinIMGroupNotification(t *testing.T) {
	got := formatDouyinIMGroupNotification("【一盆蘸酱菜|肥家】", "一盆蘸酱菜：[晚上好]", "2026-07-18 19:20:07")
	want := "【一盆蘸酱菜|肥家】\n一盆蘸酱菜：[晚上好]\n2026-07-18 19:20:07"
	if got != want {
		t.Fatalf("group notification=%q", got)
	}
}

func TestFormatDouyinPrivateNotificationHeader(t *testing.T) {
	got := formatDouyinPrivateNotificationHeader("葡萄吞十七", "葡萄吞十七(唐欣怡)", "消息正文")
	want := "【葡萄吞十七|抖音】\n葡萄吞十七(唐欣怡)：消息正文"
	if got != want {
		t.Fatalf("header=%q", got)
	}
}

func TestResolveDouyinSenderLabels(t *testing.T) {
	box, line := resolveDouyinSenderLabels(douyinBrowserEvent{
		SenderNickname: "葡萄吞十七",
		SenderRemark:   "唐欣怡",
		SenderName:     "唐欣怡",
	})
	// Title box uses 抖音昵称; body line keeps 名(备注)
	if box != "葡萄吞十七" {
		t.Fatalf("box=%q", box)
	}
	if line != "葡萄吞十七(唐欣怡)" {
		t.Fatalf("line=%q", line)
	}
	// no remark → both use nickname
	box, line = resolveDouyinSenderLabels(douyinBrowserEvent{SenderNickname: "一盆蘸酱菜"})
	if box != "一盆蘸酱菜" || line != "一盆蘸酱菜" {
		t.Fatalf("no-remark box=%q line=%q", box, line)
	}
	// equal
	if got := formatDouyinNamePair("胡晓慧", "胡晓慧"); got != "胡晓慧" {
		t.Fatalf("equal=%q", got)
	}
	// nick "胡晓慧（小包）" + remark "胡晓慧" → 正文「小包(胡晓慧)」；标题框用昵称
	box, line = resolveDouyinSenderLabels(douyinBrowserEvent{
		SenderNickname: "胡晓慧（小包）",
		SenderRemark:   "胡晓慧",
	})
	if box != "胡晓慧（小包）" || line != "小包(胡晓慧)" {
		t.Fatalf("huxiaohui box=%q line=%q", box, line)
	}
	// reverse containment (remark longer)
	if got := formatDouyinNamePair("胡晓慧", "胡晓慧（小包）"); got != "胡晓慧(小包)" {
		t.Fatalf("reverse containment=%q", got)
	}
	// already "小名(备注)" / Chinese source normalized
	if got := formatDouyinNamePair("小包（胡晓慧）", "胡晓慧"); got != "小包(胡晓慧)" {
		t.Fatalf("already ordered=%q", got)
	}
}

func TestFormatDouyinPrivateNotification(t *testing.T) {
	got := formatDouyinPrivateNotification("葡萄吞十七", "葡萄吞十七(唐欣怡)", "消息正文", "2026-07-17 10:00:00")
	want := "【葡萄吞十七|抖音】\n葡萄吞十七(唐欣怡)：消息正文\n2026-07-17 10:00:00"
	if got != want {
		t.Fatalf("notification=%q", got)
	}
	// reply stack (quote is self share card)
	replyBody := formatDouyinReplyText("葡萄吞十七(唐欣怡)", "并排呀", "我", "[分享图文]")
	got = formatDouyinPrivateNotification("葡萄吞十七", "葡萄吞十七(唐欣怡)", replyBody, "2026-07-19 08:42:02")
	want = "【葡萄吞十七|抖音】\n我：[分享图文]\n葡萄吞十七(唐欣怡)：并排呀\n2026-07-19 08:42:02"
	if got != want {
		t.Fatalf("reply notification=%q", got)
	}
}

func TestFormatDouyinReplyText(t *testing.T) {
	got := formatDouyinReplyText("发送人", "回复内容", "原发送人", "原消息")
	if got != "原发送人：原消息\n发送人：回复内容" {
		t.Fatalf("reply=%q", got)
	}
	// quoted is self
	got = formatDouyinReplyText("葡萄吞十七(唐欣怡)", "诱惑你快点去看小肥发了啥", "我", "我发的内容")
	if got != "我：我发的内容\n葡萄吞十七(唐欣怡)：诱惑你快点去看小肥发了啥" {
		t.Fatalf("self-quote reply=%q", got)
	}
}

func TestInferDouyinQuotedName(t *testing.T) {
	peerReply := douyinBrowserEvent{SenderUID: "peer", QuotedSenderUID: "peer"}
	if got := inferDouyinQuotedName(peerReply, "对方昵称", "self"); got != "对方昵称" {
		t.Fatalf("peer quoted name=%q", got)
	}
	selfReply := douyinBrowserEvent{SenderUID: "peer", SelfUID: "self", QuotedSenderUID: "self"}
	if got := inferDouyinQuotedName(selfReply, "对方昵称", "self"); got != "我" {
		t.Fatalf("self quoted name=%q", got)
	}
	explicit := douyinBrowserEvent{QuotedName: "引用昵称", QuotedSenderUID: "self"}
	if got := inferDouyinQuotedName(explicit, "对方昵称", "self"); got != "引用昵称" {
		t.Fatalf("explicit quoted name=%q", got)
	}
}

func TestClassifyDouyinIMEventRejectsExplicitSelfUID(t *testing.T) {
	event := douyinBrowserEvent{ConversationType: 1, SenderUID: "self", SelfUID: "self"}
	if got := classifyDouyinIMEvent(event, "", "", event.SelfUID); got != "" {
		t.Fatalf("self message without isSelfChat classified as %q", got)
	}
	event.IsSelfChat = true
	if got := classifyDouyinIMEvent(event, "", "", event.SelfUID); got != "private_self" {
		t.Fatalf("self-chat message classified as %q, want private_self", got)
	}
}

func TestResolveDouyinWorksDisplayName(t *testing.T) {
	// Title = raw nick; body = name pair (not stuffed into 【】).
	if got := resolveDouyinWorksTitleNick("胡晓慧（小包）", "胡晓慧"); got != "胡晓慧（小包）" {
		t.Fatalf("title nick=%q", got)
	}
	if got := formatDouyinNamePair("胡晓慧（小包）", "胡晓慧"); got != "小包(胡晓慧)" {
		t.Fatalf("body pair=%q", got)
	}
	if got := formatDouyinNamePair("一盆蘸酱菜", "卢天惠"); got != "一盆蘸酱菜(卢天惠)" {
		t.Fatalf("yipen pair=%q", got)
	}
	if got := formatDouyinNamePair("小狼hoho", "小狼hoho"); got != "小狼hoho" {
		t.Fatalf("equal pair=%q", got)
	}
}

func TestAppendTextWithQQFacesDouyinEmoji(t *testing.T) {
	// Douyin uses [尬笑]; QQ classic name is 尴尬 — alias should expand to face.
	segs := appendTextWithQQFaces(nil, "谢谢其实我真觉得明显[尬笑]")
	if len(segs) < 2 {
		t.Fatalf("want text+face, got %d segs: %#v", len(segs), segs)
	}
	// last should be face id 10
	last, ok := segs[len(segs)-1].(napcat.MessageSegment)
	if !ok || last.Type != "face" || last.Data["id"] != "10" {
		t.Fatalf("last segment=%#v", segs[len(segs)-1])
	}
	first, ok := segs[0].(napcat.MessageSegment)
	if !ok || first.Type != "text" || first.Data["text"] != "谢谢其实我真觉得明显" {
		t.Fatalf("first segment=%#v", segs[0])
	}
	// unknown bracket stays text
	segs = appendTextWithQQFaces(nil, "hello[未知表情]world")
	if len(segs) != 1 {
		t.Fatalf("unknown should stay single text, got %#v", segs)
	}
}
