package napcat

// Event represents a generic OneBot event
type Event struct {
	Time        int64  `json:"time"`
	SelfID      int64  `json:"self_id"`
	PostType    string `json:"post_type"`
	MessageType string `json:"message_type,omitempty"`
	SubType     string `json:"sub_type,omitempty"`

	// Message Event fields
	UserID     int64       `json:"user_id,omitempty"`
	GroupID    int64       `json:"group_id,omitempty"`
	Message    interface{} `json:"message,omitempty"`
	RawMessage string      `json:"raw_message,omitempty"`
	MessageID  int32       `json:"message_id,omitempty"`
	Sender     Sender      `json:"sender,omitempty"`

	// Notice Event fields
	NoticeType string `json:"notice_type,omitempty"`
	OperatorID int64  `json:"operator_id,omitempty"`

	// Meta Event fields
	MetaEventType string `json:"meta_event_type,omitempty"`
}

type Sender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card,omitempty"`
	Role     string `json:"role,omitempty"`
}

// MessageSegment represents a segment of a OneBot message
type MessageSegment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

func TextSegment(text string) MessageSegment {
	return MessageSegment{
		Type: "text",
		Data: map[string]string{"text": text},
	}
}

func ImageSegment(file string) MessageSegment {
	return MessageSegment{
		Type: "image",
		Data: map[string]string{"file": file}, // Can be URL or base64 or path
	}
}

func AtSegment(qq string) MessageSegment {
	return MessageSegment{
		Type: "at",
		Data: map[string]string{"qq": qq},
	}
}

func FaceSegment(id string) MessageSegment {
	return MessageSegment{
		Type: "face",
		Data: map[string]string{"id": id},
	}
}

func RecordSegment(file string) MessageSegment {
	return MessageSegment{
		Type: "record",
		Data: map[string]string{"file": file},
	}
}

func VideoSegment(file string, cover string) MessageSegment {
	data := map[string]string{"file": file}
	if cover != "" {
		data["cover"] = cover
	}
	return MessageSegment{
		Type: "video",
		Data: data,
	}
}

func JsonSegment(data string) MessageSegment {
	return MessageSegment{
		Type: "json",
		Data: map[string]string{"data": data},
	}
}

func ArkSegment(data string) MessageSegment {
	return MessageSegment{
		Type: "ark",
		Data: map[string]string{"data": data},
	}
}

func ShareSegment(url string, title string, content string, image string) MessageSegment {
	return MessageSegment{
		Type: "share",
		Data: map[string]string{
			"url":     url,
			"title":   title,
			"content": content,
			"image":   image,
		},
	}
}

func XmlSegment(data string) MessageSegment {
	return MessageSegment{
		Type: "xml",
		Data: map[string]string{"data": data},
	}
}

func MarkdownSegment(content string) MessageSegment {
	return MessageSegment{
		Type: "markdown",
		Data: map[string]string{"content": content},
	}
}

// APIRequest represents a OneBot API request
type APIRequest struct {
	Action string      `json:"action"`
	Params interface{} `json:"params"`
	Echo   string      `json:"echo,omitempty"`
}

// SendGroupMsgParams params for send_group_msg
type SendGroupMsgParams struct {
	GroupID int64       `json:"group_id"`
	Message interface{} `json:"message"` // string or []MessageSegment
}

// SendPrivateMsgParams params for send_private_msg
type SendPrivateMsgParams struct {
	UserID  int64       `json:"user_id"`
	Message interface{} `json:"message"`
}
