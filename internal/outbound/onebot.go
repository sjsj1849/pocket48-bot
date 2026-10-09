package outbound

import (
	"log"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
)

// OneBot adapts the neutral outbound contract to the existing NapCat client.
type OneBot struct {
	client *napcat.Client
}

func NewOneBot(client *napcat.Client) *OneBot {
	return &OneBot{client: client}
}

func (a *OneBot) Capabilities() Capabilities {
	return Capabilities{
		Platform:              "qq",
		MaxImagesPerMessage:   9,
		CanMixTextAndImages:   true,
		VideoMustBeStandalone: true,
		SupportsCards:         false,
		RichText:              RichTextNone,
	}
}

func (a *OneBot) Send(target Target, content interface{}) {
	if a == nil || a.client == nil {
		return
	}
	content = message.Normalize(message.ToSegments(content))
	for _, batch := range Plan(content, a.Capabilities()) {
		batch = toOneBot(batch)
		switch target.Kind {
		case GroupChat:
			a.client.SendGroupMessage(target.ID, batch)
		case PrivateChat:
			a.client.SendPrivateMessage(target.ID, batch)
		default:
			log.Printf("[Outbound] unsupported OneBot target kind=%q id=%d", target.Kind, target.ID)
		}
	}
}

func (a *OneBot) QueueDepth() int {
	if a == nil || a.client == nil {
		return 0
	}
	return a.client.QueueDepth()
}

func toOneBot(content interface{}) interface{} {
	switch value := content.(type) {
	case message.Segment:
		return segmentToOneBot(value)
	case []message.Segment:
		result := make([]napcat.MessageSegment, 0, len(value))
		for _, segment := range value {
			result = append(result, segmentToOneBot(segment))
		}
		return result
	case []interface{}:
		result := make([]interface{}, 0, len(value))
		for _, item := range value {
			if segment, ok := item.(message.Segment); ok {
				result = append(result, segmentToOneBot(segment))
			} else {
				result = append(result, item)
			}
		}
		return result
	default:
		return content
	}
}

func segmentToOneBot(segment message.Segment) napcat.MessageSegment {
	data := make(map[string]string, len(segment.Data))
	for key, value := range segment.Data {
		data[key] = value
	}
	switch segment.Type {
	case "mention":
		return napcat.MessageSegment{Type: "at", Data: map[string]string{"qq": data["user"]}}
	case "mention_all":
		return napcat.MessageSegment{Type: "at", Data: map[string]string{"qq": "all"}}
	case "audio":
		segment.Type = "record"
	}
	return napcat.MessageSegment{Type: segment.Type, Data: data}
}
