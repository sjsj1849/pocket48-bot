// Package message defines the platform-neutral outbound message model.
package message

// Segment is one part of an outbound message. Type names describe intent;
// adapters are responsible for translating Data to their platform protocol.
type Segment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func Text(text string) Segment {
	return Segment{Type: "text", Data: map[string]string{"text": text}}
}

func Image(file string) Segment {
	return Segment{Type: "image", Data: map[string]string{"file": file}}
}

func Mention(user string) Segment {
	return Segment{Type: "mention", Data: map[string]string{"user": user}}
}

func MentionAll() Segment {
	return Segment{Type: "mention_all", Data: map[string]string{}}
}

func Face(id string) Segment {
	return Segment{Type: "face", Data: map[string]string{"id": id}}
}

func Audio(file string) Segment {
	return Segment{Type: "audio", Data: map[string]string{"file": file}}
}

func Video(file, cover string) Segment {
	data := map[string]string{"file": file}
	if cover != "" {
		data["cover"] = cover
	}
	return Segment{Type: "video", Data: data}
}

func Rich(kind, data string) Segment {
	return Segment{Type: kind, Data: map[string]string{"data": data}}
}

func Share(url, title, content, image string) Segment {
	return Segment{Type: "share", Data: map[string]string{
		"url": url, "title": title, "content": content, "image": image,
	}}
}

// Normalize converts protocol-shaped legacy segments into neutral semantics.
// It allows a gradual call-site migration while every registered adapter sees
// the same model.
func Normalize(content interface{}) interface{} {
	switch value := content.(type) {
	case Segment:
		return normalizeSegment(value)
	case []Segment:
		result := make([]Segment, len(value))
		for i, segment := range value {
			result[i] = normalizeSegment(segment)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(value))
		for i, item := range value {
			if segment, ok := item.(Segment); ok {
				result[i] = normalizeSegment(segment)
			} else {
				result[i] = item
			}
		}
		return result
	default:
		return content
	}
}

func normalizeSegment(segment Segment) Segment {
	switch segment.Type {
	case "at":
		if segment.Data["qq"] == "all" {
			return MentionAll()
		}
		return Mention(segment.Data["qq"])
	case "record":
		return Audio(segment.Data["file"])
	default:
		return segment
	}
}
