package pocket48

import (
	"encoding/json"
	"sort"
	"strconv"
)

const (
	APIMsgOwner       = BaseURL + "/im/api/v1/team/message/list/homeowner"
	APIMsgAll         = BaseURL + "/im/api/v1/team/message/list/all"
	APIServer2Channel = BaseURL + "/im/api/v1/team/last/message/get"
	APIStar2Server    = BaseURL + "/im/api/v1/im/server/jump"
	APIUserInfo       = BaseURL + "/user/api/v2/user/info/home"
	APIStarArchives   = BaseURL + "/user/api/v1/user/star/archives"
	APISearch         = BaseURL + "/im/api/v1/im/search"
)

func (c *Client) GetUserInfo(userID int64) (*User, error) {
	endpoint := APIUserInfo
	payload := map[string]interface{}{"userId": userID}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		BaseUserInfo User `json:"baseUserInfo"`
	}
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	return &content.BaseUserInfo, nil
}

type UserDetailInfo struct {
	UserID       int64  `json:"userId"`
	Nickname     string `json:"nickname"`
	RealNickName string `json:"realNickName"`
	StarName     string `json:"starName"`
	IsStar       bool   `json:"star"`
}

func (c *Client) GetUserDetailInfo(userID int64) (*UserDetailInfo, error) {
	endpoint := APIUserInfo
	payload := map[string]interface{}{"userId": userID}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		BaseUserInfo struct {
			UserID       int64  `json:"userId"`
			Nickname     string `json:"nickname"`
			RealNickName string `json:"realNickName"`
			StarName     string `json:"starName"`
			IsStar       bool   `json:"star"`
		} `json:"baseUserInfo"`
	}
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	return &UserDetailInfo{
		UserID:       content.BaseUserInfo.UserID,
		Nickname:     content.BaseUserInfo.Nickname,
		RealNickName: content.BaseUserInfo.RealNickName,
		StarName:     content.BaseUserInfo.StarName,
		IsStar:       content.BaseUserInfo.IsStar,
	}, nil
}

type StarInfo struct {
	UserID        int64  `json:"userId"`
	StarName      string `json:"starName"`
	StarAvatar    string `json:"starAvatar"`
	StarGroupName string `json:"starGroupName"`
	StarTeamName  string `json:"starTeamName"`
	PeriodName    string `json:"periodName"`
	Nickname      string `json:"nickname"`
	JoinTime      string `json:"joinTime"`
	Height        string `json:"height"`
	BloodType     string `json:"bloodType"`
	Birthday      string `json:"birthday"`
	Constellation string `json:"constellation"`
	Birthplace    string `json:"birthplace"`
	Specialty     string `json:"specialty"`
	Hobbies       string `json:"hobbies"`
}

func (c *Client) GetStarArchives(memberID int64) (*StarInfo, error) {
	endpoint := APIStarArchives
	payload := map[string]interface{}{"memberId": memberID}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		StarInfo StarInfo `json:"starInfo"`
	}
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	return &content.StarInfo, nil
}

func (c *Client) GetJumpContent(starID int64) (int64, int64, error) {
	endpoint := APIStar2Server
	payload := map[string]interface{}{"starId": starID, "targetType": 1}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return 0, 0, err
	}

	var content struct {
		ChannelID      int64 `json:"channelId"`
		JumpServerInfo struct {
			ServerID int64 `json:"serverId"`
		} `json:"jumpServerInfo"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return 0, 0, err
	}

	return content.ChannelID, content.JumpServerInfo.ServerID, nil
}

func (c *Client) GetChannelIDByServerID(serverID int64) ([]int64, error) {
	endpoint := APIServer2Channel
	payload := map[string]string{"serverId": strconv.FormatInt(serverID, 10)}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		LastMsgList []struct {
			ChannelID int64 `json:"channelId"`
		} `json:"lastMsgList"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	ids := make([]int64, len(content.LastMsgList))
	for i, v := range content.LastMsgList {
		ids[i] = v.ChannelID
	}
	return ids, nil
}

func (c *Client) GetMessages(roomInfo *RoomInfo, limit int) ([]*Message, error) {
	return c.getMessagesFromEndpoint(APIMsgOwner, roomInfo, limit)
}

func (c *Client) GetAllMessages(roomInfo *RoomInfo, limit int) ([]*Message, error) {
	return c.getMessagesFromEndpoint(APIMsgAll, roomInfo, limit)
}

func (c *Client) getMessagesFromEndpoint(endpoint string, roomInfo *RoomInfo, limit int) ([]*Message, error) {
	payload := map[string]interface{}{
		"nextTime":  0,
		"serverId":  roomInfo.ServerID,
		"channelId": roomInfo.ChannelID,
		"limit":     limit,
	}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		Message []RawMessage `json:"message"`
	}
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	var msgs []*Message
	for _, raw := range content.Message {
		msg := &Message{
			Room:        roomInfo,
			Type:        MessageType(raw.MsgType),
			Body:        raw.Bodys,
			Time:        raw.MsgTime,
			MsgIDServer: raw.MsgIDServer,
			MsgIDClient: raw.MsgIDClient,
			RawExt:      string(raw.ExtInfo),
		}

		var ext ExtInfo
		if len(raw.ExtInfo) > 0 {
			// raw.ExtInfo is a JSON string containing escaped JSON, need to unmarshal twice
			var extStr string
			if err := json.Unmarshal(raw.ExtInfo, &extStr); err == nil {
				if err := json.Unmarshal([]byte(extStr), &ext); err != nil {
				} else {
				}
			} else {
			}
		}
		msg.ExtInfo = ext
		msg.NickName = ext.User.Nickname

		msgs = append(msgs, msg)
	}

	// Sort messages by time descending (newest first) to handle potential API disorder
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Time > msgs[j].Time
	})

	return msgs, nil
}

func (c *Client) Search(query string) ([]SearchServerInfo, error) {
	endpoint := BaseURL + "/im/api/v1/im/server/search"
	payload := map[string]string{"searchContent": query}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		ServerApiList []SearchServerInfo `json:"serverApiList"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}
	return content.ServerApiList, nil
}

func (c *Client) GetRoomVoiceList(roomID, serverID int64) ([]int64, error) {
	endpoint := BaseURL + "/im/api/v1/team/voice/operate"
	payload := map[string]interface{}{
		"channelId":   roomID,
		"serverId":    serverID,
		"operateCode": 2,
	}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		VoiceUserList []struct {
			UserID int64 `json:"userId"`
		} `json:"voiceUserList"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	ids := make([]int64, len(content.VoiceUserList))
	for i, v := range content.VoiceUserList {
		ids[i] = v.UserID
	}
	return ids, nil
}

func (c *Client) GetLiveList() ([]LiveListItem, error) {
	endpoint := BaseURL + "/live/api/v1/live/getLiveList"
	payload := map[string]interface{}{
		"groupId": 0,
		"debug":   true,
		"next":    0,
		"record":  false,
	}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		LiveList []LiveListItem `json:"liveList"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}
	return content.LiveList, nil
}

func (c *Client) GetLiveOne(liveID string) (*LiveOne, error) {
	endpoint := BaseURL + "/live/api/v1/live/getLiveOne"
	payload := map[string]interface{}{
		"liveId": liveID,
	}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content LiveOne
	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	return &content, nil
}

func (c *Client) GetRoomInfoByChannelID(roomID int64) (*RoomInfo, error) {
	endpoint := BaseURL + "/im/api/v1/im/team/room/info"
	payload := map[string]string{"channelId": strconv.FormatInt(roomID, 10)}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		ChannelInfo RoomInfo `json:"channelInfo"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	info := &content.ChannelInfo

	// Fix: If ChannelName is "直播", try to find the real name via Search
	if info.ChannelName == "直播" {
		// Attempt fallback default first
		info.ChannelName = info.OwnerName + "的房间"

		// Try to find better name via Search
		results, err := c.Search(info.OwnerName)
		if err == nil {
			for _, res := range results {
				if res.ServerID == info.ServerID {
					info.ChannelName = res.ServerName
					break
				}
			}
		}
	}

	return info, nil
}

func (c *Client) GetTeamChannels(serverID int64) ([]TeamChannel, error) {
	endpoint := BaseURL + "/im/api/v1/team/channel/list"
	payload := map[string]string{"teamId": strconv.FormatInt(serverID, 10)}

	resp, err := c.Post(endpoint, payload)
	if err != nil {
		return nil, err
	}

	var content struct {
		ChannelList []TeamChannel `json:"channelList"`
	}

	if err := json.Unmarshal(resp.Content, &content); err != nil {
		return nil, err
	}

	return content.ChannelList, nil
}