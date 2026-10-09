package napcat

import (
	"strings"
	"testing"

	"pocket48-bot/internal/config"
)

func TestSendGroupMessageKeepsAtAllWithTypedBody(t *testing.T) {
	client := NewClient(&config.Config{})
	client.SendGroupMessage(123, []MessageSegment{
		AtSegment("all"),
		TextSegment("\n"),
		TextSegment("【成员|Pocket48】\n正文"),
	})

	message := takeGroupMessage(t, client)
	segments, ok := message.([]MessageSegment)
	if !ok || len(segments) != 3 || segments[0].Data["text"] != "【成员|Pocket48】\n" || !isAtAll(segments[1]) || segments[2].Data["text"] != "\n正文" {
		t.Fatalf("message = %#v, want title, at-all and body in one message", message)
	}
	if len(client.sendChan) != 0 {
		t.Fatalf("queued requests = %d, want one send_group_msg", len(client.sendChan))
	}
}

func TestSendGroupMessageKeepsAtAllWithInterfaceBody(t *testing.T) {
	client := NewClient(&config.Config{})
	client.SendGroupMessage(456, []interface{}{
		AtSegment("all"),
		TextSegment("\r\n【成员|Pocket48】\n正文"),
		ImageSegment("https://example.com/image.jpg"),
	})

	message := takeGroupMessage(t, client)
	segments, ok := message.([]interface{})
	if !ok || len(segments) != 4 {
		t.Fatalf("message = %#v, want title, at-all, body and image", message)
	}
	if segments[0].(MessageSegment).Data["text"] != "【成员|Pocket48】\n" || !isAtAll(segments[1].(MessageSegment)) || segments[2].(MessageSegment).Data["text"] != "\n正文" || segments[3].(MessageSegment).Type != "image" {
		t.Fatalf("message layout = %#v", message)
	}
	// If QQ consumes the at-all quota and drops the at segment, the title still
	// occupies line one instead of leaving an empty first line.
	if strings.HasPrefix(segments[0].(MessageSegment).Data["text"], "\n") {
		t.Fatal("message can start with a blank line when at-all is dropped")
	}
}

func TestSendGroupMessageLeavesOrdinaryMessagesUnchanged(t *testing.T) {
	client := NewClient(&config.Config{})
	want := []MessageSegment{AtSegment("10001"), TextSegment(" 你好")}
	client.SendGroupMessage(789, want)

	got := takeGroupMessage(t, client)
	segments, ok := got.([]MessageSegment)
	if !ok || len(segments) != 2 || segments[0].Data["qq"] != "10001" || segments[1].Data["text"] != " 你好" {
		t.Fatalf("message = %#v, want unchanged ordinary mention", got)
	}
	if len(client.sendChan) != 0 {
		t.Fatalf("queued requests = %d, want 0 after one request", len(client.sendChan))
	}
}

func takeGroupMessage(t *testing.T, client *Client) interface{} {
	t.Helper()
	select {
	case request := <-client.sendChan:
		params, ok := request.Params.(SendGroupMsgParams)
		if !ok {
			t.Fatalf("params = %#v, want SendGroupMsgParams", request.Params)
		}
		return params.Message
	default:
		t.Fatal("no queued group message")
		return nil
	}
}
