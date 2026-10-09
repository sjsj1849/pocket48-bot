package outbound

import (
	"reflect"
	"testing"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
)

func TestToOneBotTranslatesNeutralSegments(t *testing.T) {
	input := []message.Segment{
		message.Text("hello"),
		message.Mention("42"),
		message.MentionAll(),
		message.Audio("voice.mp3"),
		message.Image("image.jpg"),
	}
	want := []napcat.MessageSegment{
		napcat.TextSegment("hello"),
		napcat.AtSegment("42"),
		napcat.AtSegment("all"),
		napcat.RecordSegment("voice.mp3"),
		napcat.ImageSegment("image.jpg"),
	}
	if got := toOneBot(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("translated message = %#v, want %#v", got, want)
	}
}

type recordingSender struct {
	target  Target
	content interface{}
}

func (s *recordingSender) Send(target Target, content interface{}) {
	s.target, s.content = target, content
}

func (*recordingSender) QueueDepth() int { return 0 }

func TestConvenienceSendersUseNeutralTargets(t *testing.T) {
	sender := &recordingSender{}
	SendGroup(sender, 123, message.Text("group"))
	if sender.target.Kind != GroupChat || sender.target.ID != 123 {
		t.Fatalf("group target = %#v", sender.target)
	}
	SendPrivate(sender, 456, message.Text("private"))
	if sender.target.Kind != PrivateChat || sender.target.ID != 456 {
		t.Fatalf("private target = %#v", sender.target)
	}
}

func TestHubRoutesDefaultAndExplicitPlatforms(t *testing.T) {
	qq := &recordingSender{}
	feishu := &recordingSender{}
	hub := NewHub("qq")
	hub.Register("qq", qq)
	hub.Register("feishu", feishu)

	hub.Send(Group(123), message.Text("default"))
	if qq.target.Platform != "qq" || qq.target.ID != 123 {
		t.Fatalf("default route = %#v", qq.target)
	}
	hub.Send(Target{Platform: "feishu", Kind: GroupChat, ID: 456}, message.Text("explicit"))
	if feishu.target.Platform != "feishu" || feishu.target.ID != 456 {
		t.Fatalf("explicit route = %#v", feishu.target)
	}
}

func TestHubNormalizesLegacyOneBotSegmentsBeforeRouting(t *testing.T) {
	sender := &recordingSender{}
	hub := NewHub("feishu")
	hub.Register("feishu", sender)
	hub.Send(Group(1), []napcat.MessageSegment{napcat.AtSegment("all"), napcat.RecordSegment("voice.mp3")})

	got, ok := sender.content.([]message.Segment)
	if !ok || len(got) != 2 || got[0].Type != "mention_all" || got[1].Type != "audio" {
		t.Fatalf("normalized content = %#v", sender.content)
	}
}
