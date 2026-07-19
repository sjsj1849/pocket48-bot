package pocket48

import (
	"encoding/json"
	"errors"
	"testing"

	"pocket48-bot/internal/config"
)

func TestLiveModelsAcceptStringEncodedNumbers(t *testing.T) {
	var listItem LiveListItem
	if err := json.Unmarshal([]byte(`{"liveId":"live-1","status":"1","ctime":"1784210000","memberId":"63559","userId":63559,"roomId":"123456"}`), &listItem); err != nil {
		t.Fatal(err)
	}
	if listItem.StartTime != 1784210000 || listItem.MemberID != 63559 || listItem.LiveRoomID != 123456 || listItem.LiveStatus != 1 {
		t.Fatalf("list item = %#v", listItem)
	}

	var live LiveOne
	if err := json.Unmarshal([]byte(`{"liveId":"live-1","ctime":"1784210000","onlineNum":"88","liveType":"1","roomId":"123456","user":{"userId":"63559","roomId":"1279287"}}`), &live); err != nil {
		t.Fatal(err)
	}
	if live.RoomID != 123456 || live.OnlineNum != 88 || live.User.UserID != 63559 || live.User.RoomID != 1279287 {
		t.Fatalf("live = %#v", live)
	}
}

func TestIsAuthorizationExpired(t *testing.T) {
	if !IsAuthorizationExpired(&APIError{Status: 401003, Message: "expired"}) {
		t.Fatal("expected 401003 to be recognized as expired authorization")
	}
	if IsAuthorizationExpired(&APIError{Status: 500, Message: "failed"}) {
		t.Fatal("did not expect other API errors to be recognized as expired authorization")
	}
	if IsAuthorizationExpired(errors.New("API error: expired (status: 401003)")) {
		t.Fatal("did not expect unstructured errors to be recognized as expired authorization")
	}
}

func TestIsInconclusiveTokenCheck(t *testing.T) {
	if !IsInconclusiveTokenCheck(&APIError{Status: 500, Message: "频道不存在"}) {
		t.Fatal("expected 频道不存在 to be inconclusive")
	}
	if IsInconclusiveTokenCheck(&APIError{Status: 401003, Message: "expired"}) {
		t.Fatal("did not expect true auth expiry to be inconclusive")
	}
}

func TestIsPasswordLoginSMSRequired(t *testing.T) {
	if !IsPasswordLoginSMSRequired(&APIError{Status: 500, Message: "请使用手机号验证码登录#003"}) {
		t.Fatal("expected SMS-required password error")
	}
	if IsPasswordLoginSMSRequired(&APIError{Status: 500, Message: "password wrong"}) {
		t.Fatal("did not expect generic password error")
	}
}

func TestAuthHeadersCanExcludeExpiredToken(t *testing.T) {
	client := NewClient(&config.Config{PocketToken: "expired-token"})
	if got := client.getHeaders(true)["token"]; got != "expired-token" {
		t.Fatalf("expected token header, got %q", got)
	}
	if _, ok := client.getHeaders(false)["token"]; ok {
		t.Fatal("did not expect token header on authentication requests")
	}
}
