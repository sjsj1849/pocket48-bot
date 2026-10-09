package monitor

import (
	"encoding/json"
	"testing"
)

func TestCollectWeiboVideosIncludesLivePhotoMotion(t *testing.T) {
	var card WeiboCard
	if err := json.Unmarshal([]byte(`{
		"pics":[{
			"type":"livephoto",
			"videoSrc":"https://video.example/live.mp4",
			"large":{"url":"https://image.example/still.jpg"}
		}]
	}`), &card); err != nil {
		t.Fatal(err)
	}
	videos := collectWeiboVideos(card)
	if len(videos) != 1 || videos[0].URL != "https://video.example/live.mp4" || videos[0].Cover != "https://image.example/still.jpg" {
		t.Fatalf("live photo videos=%#v", videos)
	}
}
