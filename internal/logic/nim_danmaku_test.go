package logic

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"pocket48-bot/internal/pocket48"
)

func TestParseSidecarCommand(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "path only", line: `./sidecar/nim-bridge/index.mjs`, want: []string{"node", "./sidecar/nim-bridge/index.mjs"}},
		{name: "explicit node", line: `node ./sidecar/nim-bridge/index.mjs`, want: []string{"node", "./sidecar/nim-bridge/index.mjs"}},
		{name: "quoted path", line: `node "C:\Program Files\nim bridge\index.mjs"`, want: []string{"node", `C:\Program Files\nim bridge\index.mjs`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSidecarCommand(tt.line)
			if err != nil {
				t.Fatalf("parseSidecarCommand() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseSidecarCommand() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLiveEventUsesPocketRoomID(t *testing.T) {
	evt := sidecarEvent{RoomID: 12345, NIMRoomID: 98765}
	if got := eventPocketRoomID(evt); got != 12345 {
		t.Fatalf("eventPocketRoomID() = %d, want 12345", got)
	}
}

func TestResolveNIMLiveRoomIDPrefersGetLiveOne(t *testing.T) {
	got, err := resolveNIMLiveRoomID("live-1", 111, func(liveID string) (*pocket48.LiveOne, error) {
		if liveID != "live-1" {
			t.Fatalf("unexpected live id %q", liveID)
		}
		return &pocket48.LiveOne{RoomID: 3866824}, nil
	})
	if err != nil || got != 3866824 {
		t.Fatalf("resolveNIMLiveRoomID() = (%d, %v), want (3866824, nil)", got, err)
	}
}

func TestResolveNIMLiveRoomIDFallsBackToPushValue(t *testing.T) {
	got, err := resolveNIMLiveRoomID("live-1", 222, func(string) (*pocket48.LiveOne, error) {
		return nil, errors.New("temporary API failure")
	})
	if err != nil || got != 222 {
		t.Fatalf("resolveNIMLiveRoomID() = (%d, %v), want (222, nil)", got, err)
	}
}

func TestBridgeTracksMultipleLiveBindings(t *testing.T) {
	bridge := &NimDanmakuBridge{liveBindings: map[int64]int64{9001: 101, 9002: 202}}
	if !bridge.HasLiveBinding(101, 9001) || !bridge.HasLiveBinding(202, 9002) {
		t.Fatal("multiple live bindings were not retained")
	}
	if bridge.HasLiveBinding(101, 9002) {
		t.Fatal("NIM room was routed to the wrong Pocket48 room")
	}
}

func TestRoomRealtimeActiveRequiresRecentMessageForPocketRoom(t *testing.T) {
	bridge := &NimDanmakuBridge{
		qchatConnected:    true,
		lastRealtimeMsgAt: make(map[int64]time.Time),
	}
	if bridge.RoomRealtimeActive(101) {
		t.Fatal("connected QChat without delivered messages must keep REST polling active")
	}

	bridge.lastRealtimeMsgAt[101] = time.Now()
	if !bridge.RoomRealtimeActive(101) {
		t.Fatal("recent realtime delivery for the Pocket48 room was not considered active")
	}
	if bridge.RoomRealtimeActive(202) {
		t.Fatal("realtime activity leaked to a different Pocket48 room")
	}

	bridge.lastRealtimeMsgAt[101] = time.Now().Add(-6 * time.Minute)
	if bridge.RoomRealtimeActive(101) {
		t.Fatal("stale realtime delivery must fall back to REST polling")
	}
}

func TestMessageDedupKeepsSameIDInDifferentRooms(t *testing.T) {
	bot := &Bot{seenMessageIDs: make(map[string]time.Time)}
	first := &pocket48.Message{Room: &pocket48.RoomInfo{ChannelID: 101}, MsgIDServer: "same-id"}
	duplicate := &pocket48.Message{Room: &pocket48.RoomInfo{ChannelID: 101}, MsgIDServer: "same-id"}
	otherRoom := &pocket48.Message{Room: &pocket48.RoomInfo{ChannelID: 202}, MsgIDServer: "same-id"}
	if !bot.markMessageSeen(first) || bot.markMessageSeen(duplicate) || !bot.markMessageSeen(otherRoom) {
		t.Fatal("message ID deduplication did not respect room boundaries")
	}
}

func TestRoomRealtimeMessageToPocket(t *testing.T) {
	raw := RoomRealtimeMessage{
		ServerID:  11,
		ChannelID: 22,
		Type:      "custom",
		Time:      1710000000000,
		IDServer:  "server-id",
		IDClient:  "client-id",
		Attach:    []byte(`{"messageType":"REPLY","replyInfo":{"text":"回答"}}`),
		Ext:       []byte(`{"user":{"userId":42,"nickName":"测试成员","roleId":3}}`),
	}

	msg, err := raw.toPocketMessage(nil)
	if err != nil {
		t.Fatalf("toPocketMessage() error = %v", err)
	}
	if msg.Type != "REPLY" || msg.NickName != "测试成员" || msg.ExtInfo.User.UserID != 42 {
		t.Fatalf("unexpected message: %#v", msg)
	}
}

func TestLiveSessionAggregatesGiftScoreAndPeakOnline(t *testing.T) {
	bot := &Bot{liveSessions: make(map[int64]*LiveGiftSession)}
	room := &pocket48.RoomInfo{ChannelID: 123, OwnerID: 456, OwnerName: "测试成员"}
	if !bot.beginLiveSession(room, "live-1", 789, 12) {
		t.Fatal("new live session was not started")
	}
	bot.handleLiveUpdate(123, &LiveUpdate{OnlineNum: 34})
	bot.handleDanmakuGift(123, &GiftMessage{
		GiftName: "测试礼物",
		GiftNum:  2,
		Raw:      []byte(`{"giftInfo":{"giftName":"测试礼物","giftNum":2,"chickenLeg":5,"isScore":true,"tpNum":3}}`),
	})

	session := bot.liveSessions[123]
	if session == nil || session.ChickenLegs != 10 || session.AnnualScore != 6 || session.PeakOnline != 34 {
		t.Fatalf("unexpected live statistics: %#v", session)
	}
}

func TestBeginLiveSessionDoesNotResetSameLive(t *testing.T) {
	bot := &Bot{liveSessions: make(map[int64]*LiveGiftSession)}
	room := &pocket48.RoomInfo{ChannelID: 123, OwnerName: "测试成员"}
	bot.beginLiveSession(room, "live-1", 789, 10)
	bot.liveSessions[123].ChickenLegs = 99
	bot.beginLiveSession(room, "live-1", 789, 20)
	if got := bot.liveSessions[123]; got.ChickenLegs != 99 || got.PeakOnline != 20 {
		t.Fatalf("same live was reset: %#v", got)
	}
}
