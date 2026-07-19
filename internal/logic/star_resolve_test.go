package logic

import (
	"testing"
	"time"

	"pocket48-bot/internal/pocket48"
)

func TestIsPocketNotFound(t *testing.T) {
	if !isPocketNotFound(&pocket48.APIError{Status: 404, Message: "No message available"}) {
		t.Fatal("404 should be not found")
	}
	if !isPocketNotFound(&pocket48.APIError{Status: 400, Message: "成员不存在"}) {
		t.Fatal("成员不存在 should be not found")
	}
	if isPocketNotFound(&pocket48.APIError{Status: 500, Message: "boom"}) {
		t.Fatal("500 should not be not found")
	}
}

func TestCacheUserDetailNegative(t *testing.T) {
	bot := &Bot{userDetailCache: map[int64]cachedUserDetail{}}
	bot.cacheUserDetail(116458047, &pocket48.UserDetailInfo{UserID: 116458047}, false, time.Now().Add(time.Minute))
	if bot.isKnownStar(116458047) {
		t.Fatal("negative cache should be non-star")
	}
}
