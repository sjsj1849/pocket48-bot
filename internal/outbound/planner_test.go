package outbound

import (
	"testing"

	"pocket48-bot/internal/message"
)

func TestPlanSeparatesVideoAndLimitsImages(t *testing.T) {
	content := []message.Segment{message.Text("title")}
	for i := 0; i < 10; i++ {
		content = append(content, message.Image("image"))
	}
	content = append(content, message.Video("video", ""), message.Text("footer"))
	batches := Plan(content, Capabilities{
		MaxImagesPerMessage:   9,
		CanMixTextAndImages:   true,
		VideoMustBeStandalone: true,
	})
	if len(batches) != 4 {
		t.Fatalf("batch count = %d, want 4: %#v", len(batches), batches)
	}
	video := batches[2].([]message.Segment)
	if len(video) != 1 || video[0].Type != "video" {
		t.Fatalf("video batch = %#v", video)
	}
}

func TestPlanSeparatesTextAndImagesWhenPlatformCannotMix(t *testing.T) {
	batches := Plan([]message.Segment{
		message.Text("title"), message.Image("image"), message.Text("footer"),
	}, Capabilities{MaxImagesPerMessage: 1, CanMixTextAndImages: false})
	if len(batches) != 3 {
		t.Fatalf("batch count = %d, want 3: %#v", len(batches), batches)
	}
}
