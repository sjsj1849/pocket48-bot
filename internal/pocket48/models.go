package pocket48

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Response represents a generic Pocket48 API response
type Response struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Content json.RawMessage `json:"content"`
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error: %s (status: %d)", e.Message, e.Status)
}

func IsAuthorizationExpired(err error) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.Status == 401003
}

// IsInconclusiveTokenCheck reports probe failures that do not prove the token is dead.
// CheckToken hits a dummy channel; Pocket often answers "频道不存在" even with a valid token.
func IsInconclusiveTokenCheck(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	msg := strings.TrimSpace(apiErr.Message)
	if msg == "" {
		return false
	}
	return strings.Contains(msg, "频道不存在") ||
		strings.Contains(strings.ToLower(msg), "channel not") ||
		strings.Contains(msg, "channelId")
}

// IsPasswordLoginSMSRequired is true when the server rejects MOBILE_PWD and demands SMS.
func IsPasswordLoginSMSRequired(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return strings.Contains(err.Error(), "请使用手机号验证码登录")
	}
	return strings.Contains(apiErr.Message, "请使用手机号验证码登录") ||
		strings.Contains(apiErr.Message, "验证码登录")
}

// MessageType represents the type of a Pocket48 message
type MessageType string

const (
	MsgText               MessageType = "TEXT"
	MsgGiftText           MessageType = "GIFT_TEXT"
	MsgAudio              MessageType = "AUDIO"
	MsgImage              MessageType = "IMAGE"
	MsgVideo              MessageType = "VIDEO"
	MsgExpressImage       MessageType = "EXPRESSIMAGE"
	MsgReply              MessageType = "REPLY"
	MsgGiftReply          MessageType = "GIFTREPLY"
	MsgAudioGiftReply     MessageType = "AUDIO_GIFT_REPLY"
	MsgLivePush           MessageType = "LIVEPUSH"
	MsgShareLive          MessageType = "SHARE_LIVE"
	MsgFlipCard           MessageType = "FLIPCARD"
	MsgFlipCardAudio      MessageType = "FLIPCARD_AUDIO"
	MsgFlipCardVideo      MessageType = "FLIPCARD_VIDEO"
	MsgPasswordRedPackage MessageType = "PASSWORD_REDPACKAGE"
	MsgVote               MessageType = "VOTE"
	MsgSharePosts         MessageType = "SHARE_POSTS"
)

// RoomInfo represents a Pocket48 room (channel)
type RoomInfo struct {
	ChannelName string `json:"channelName"`
	OwnerName   string `json:"ownerName"`
	OwnerID     int64  `json:"ownerId"`
	ServerID    int64  `json:"serverId"`
	ChannelID   int64  `json:"channelId"`
	StarID      int64  `json:"-"` // Set separately
	BgImg       string `json:"-"` // Set separately
	IsLiveRoom  bool   // Set by caller; true if original channel type is live/直播
}

// User represents a Pocket48 user
type User struct {
	UserID   int64  `json:"userId"`
	Nickname string `json:"nickName"`
	Avatar   string `json:"avatar"`
	IsStar   bool   `json:"isStar"`
	Level    int    `json:"level,omitempty"`
	RoleId   int    `json:"roleId,omitempty"`
	Vip      bool   `json:"vip,omitempty"`
	TeamLogo string `json:"teamLogo,omitempty"`
	PfUrl    string `json:"pfUrl,omitempty"`
}

// Message represents a Pocket48 message (converted from API)
type Message struct {
	Room        *RoomInfo   `json:"room"`
	MsgIDServer string      `json:"msgIdServer"`
	MsgIDClient string      `json:"msgIdClient"`
	NickName    string      `json:"nickName"`
	StarName    string      `json:"starName"`
	Type        MessageType `json:"type"`
	Body        string      `json:"body"`
	Time        int64       `json:"time"`
	RawExt      string      `json:"-"`
	DirectMedia bool        `json:"-"`
	ExtInfo     ExtInfo     `json:"extInfo,omitempty"`
}

type ExtInfo struct {
	User        User   `json:"user"`
	Reference   *Reply `json:"reply,omitempty"`
	BubbleID    string `json:"bubbleId,omitempty"`
	ChannelRole string `json:"channelRole,omitempty"`
}

// RawMessage represents the raw JSON object from the message list API
type RawMessage struct {
	MsgIDServer string          `json:"msgIdServer"`
	MsgIDClient string          `json:"msgIdClient"`
	MsgType     string          `json:"msgType"`
	Bodys       string          `json:"bodys"`
	MsgTime     int64           `json:"msgTime"`
	ExtInfo     json.RawMessage `json:"extInfo"`
}

// Reply represents a reply or gift reply
type Reply struct {
	ReplyName      string `json:"replyName"`
	ReplyText      string `json:"replyText"`
	Text           string `json:"text"`
	IsGift         bool   `json:"isGift"`
	ReplyMessageId string `json:"replyMessageId,omitempty"`
}

// LivePush represents a live stream notification
type LivePush struct {
	Cover     string `json:"liveCover"`
	Title     string `json:"liveTitle"`
	ID        string `json:"liveId"`
	ShortPath string `json:"shortPath,omitempty"`
}

// Answer represents a flip card answer
type Answer struct {
	Question   string      `json:"question"`
	Answer     string      `json:"answer"`
	AnswerID   string      `json:"answerId"`
	QuestionID string      `json:"questionId"`
	Type       MessageType `json:"type"`
	PreviewImg string      `json:"previewImg"`
	ResInfo    string      `json:"resInfo"`
	Ext        string      `json:"ext"`
	MsgTo      string      `json:"msgTo"` // Original message sent by fan
}

// SearchServerInfo represents a server from search results
type SearchServerInfo struct {
	ServerID          int64  `json:"serverId"`
	ServerName        string `json:"serverName"`
	ServerIcon        string `json:"serverIcon"`
	FollowStatus      int    `json:"followStatus"`
	ShowButton        bool   `json:"showButton"`
	ServerDefaultName string `json:"serverDefaultName"`
	ServerDefaultIcon string `json:"serverDefaultIcon"`
	ServerType        int    `json:"serverType"`
	ServerOwner       int64  `json:"serverOwner"`
	TeamID            int64  `json:"teamId"`
}

type TeamChannel struct {
	ChannelID   int64  `json:"channelId"`
	ChannelName string `json:"channelName"`
	ChannelType string `json:"channelType"`
	SubTitle    string `json:"subTitle"`
}

type LiveUser struct {
	UserID   int64  `json:"userId"`
	Nickname string `json:"nickName"`
	StarName string `json:"starName"`
	RoomID   int64  `json:"roomId"`
	Avatar   string `json:"avatar"`
}

type flexibleInt64 int64

func (value *flexibleInt64) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" || raw == `""` {
		*value = 0
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		raw = strings.TrimSpace(text)
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	*value = flexibleInt64(parsed)
	return nil
}

func (user *LiveUser) UnmarshalJSON(data []byte) error {
	type alias LiveUser
	aux := struct {
		*alias
		UserID flexibleInt64 `json:"userId"`
		RoomID flexibleInt64 `json:"roomId"`
	}{alias: (*alias)(user)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	user.UserID = int64(aux.UserID)
	user.RoomID = int64(aux.RoomID)
	return nil
}

type LiveListItem struct {
	LiveID      string `json:"liveId"`
	LiveTitle   string `json:"title"`
	LiveCover   string `json:"cover"`
	LiveStatus  int    `json:"status"`
	StartTime   int64  `json:"ctime"`
	MemberID    int64  `json:"memberId"`
	UserID      int64  `json:"userId"`
	MemberName  string `json:"memberName"`
	NickName    string `json:"nickName"`
	LiveRoomID  int64  `json:"roomId"`
	MsgFilePath string `json:"msgFilePath"`
}

func (item *LiveListItem) UnmarshalJSON(data []byte) error {
	type alias LiveListItem
	aux := struct {
		*alias
		LiveStatus flexibleInt64 `json:"status"`
		StartTime  flexibleInt64 `json:"ctime"`
		MemberID   flexibleInt64 `json:"memberId"`
		UserID     flexibleInt64 `json:"userId"`
		LiveRoomID flexibleInt64 `json:"roomId"`
	}{alias: (*alias)(item)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	item.LiveStatus = int(aux.LiveStatus)
	item.StartTime = int64(aux.StartTime)
	item.MemberID = int64(aux.MemberID)
	item.UserID = int64(aux.UserID)
	item.LiveRoomID = int64(aux.LiveRoomID)
	return nil
}

type LiveOne struct {
	LiveID      string   `json:"liveId"`
	Title       string   `json:"title"`
	Cover       string   `json:"cover"`
	Ctime       int64    `json:"ctime"`
	OnlineNum   int64    `json:"onlineNum"`
	MsgFilePath string   `json:"msgFilePath"`
	LiveType    int      `json:"liveType"`
	RoomID      int64    `json:"roomId"`
	User        LiveUser `json:"user"`
}

func (item *LiveOne) UnmarshalJSON(data []byte) error {
	type alias LiveOne
	aux := struct {
		*alias
		Ctime     flexibleInt64 `json:"ctime"`
		OnlineNum flexibleInt64 `json:"onlineNum"`
		LiveType  flexibleInt64 `json:"liveType"`
		RoomID    flexibleInt64 `json:"roomId"`
	}{alias: (*alias)(item)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	item.Ctime = int64(aux.Ctime)
	item.OnlineNum = int64(aux.OnlineNum)
	item.LiveType = int(aux.LiveType)
	item.RoomID = int64(aux.RoomID)
	return nil
}

// LoginResponseContent is the content of a successful login response
type LoginResponseContent struct {
	Token    string `json:"token"`
	UserInfo User   `json:"userInfo"`
}

// IMUserInfo contains the per-user credentials returned by /im/userinfo.
// Pwd is the NIM login token, not the Pocket48 account password.
type IMUserInfo struct {
	AccID string `json:"accid"`
	Pwd   string `json:"pwd"`
}
