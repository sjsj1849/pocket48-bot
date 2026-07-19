package admin

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/pocket48"
)

type pocketRoomSearchResult struct {
	ServerID    int64  `json:"serverId"`
	ServerName  string `json:"serverName"`
	OwnerName   string `json:"ownerName,omitempty"`
	RoomID      int64  `json:"roomId"`
	ChannelName string `json:"channelName"`
	Status      string `json:"status"` // open | closed | unknown
	Note        string `json:"note"`
	LastMsgAt   string `json:"lastMsgAt,omitempty"`
	LastMsgAgo  string `json:"lastMsgAgo,omitempty"`
	IsLiveRoom  bool   `json:"isLiveRoom"` // true if the channel is a live/直播 room, not a text chat
}

func (s *Server) handlePocketRoomSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请输入要搜索的小偶像名字"})
		return
	}
	cfg, err := config.LoadConfig(s.opts.ConfigPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "读取配置失败: " + err.Error()})
		return
	}
	if strings.TrimSpace(cfg.PocketToken) == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "未配置 POCKET_TOKEN，无法搜索口袋房间"})
		return
	}
	client := pocket48.NewClient(cfg)
	servers, err := client.Search(q)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "搜索失败: " + err.Error()})
		return
	}
	results := make([]pocketRoomSearchResult, 0)
	// Cap probes to keep panel snappy.
	const maxServers = 8
	const maxRoomsPerServer = 6
	limit := len(servers)
	if limit > maxServers {
		limit = maxServers
	}
	for _, server := range servers[:limit] {
		roomIDs, err := client.GetChannelIDByServerID(server.ServerID)
		if err != nil || len(roomIDs) == 0 {
			results = append(results, pocketRoomSearchResult{
				ServerID:   server.ServerID,
				ServerName: server.ServerName,
				Status:     "unknown",
				Note:       "未能解析频道列表",
			})
			continue
		}
		n := len(roomIDs)
		if n > maxRoomsPerServer {
			n = maxRoomsPerServer
		}
		for _, roomID := range roomIDs[:n] {
			item := pocketRoomSearchResult{
				ServerID:   server.ServerID,
				ServerName: server.ServerName,
				RoomID:     roomID,
				Status:     "unknown",
				Note:       "状态未知",
			}
			info, infoErr := client.GetRoomInfoByChannelID(roomID)
			if infoErr != nil || info == nil || info.ChannelID == 0 {
				item.Status = "closed"
				item.Note = "房间已关闭或请求不到"
				item.ChannelName = server.ServerName
				results = append(results, item)
				continue
			}
			item.ChannelName = info.ChannelName
			item.OwnerName = info.OwnerName
			// Detect live rooms: api.go renames "直播" → serverName, but we keep original field
			if info.IsLiveRoom || info.ChannelName == "直播" {
				item.IsLiveRoom = true
				item.Note = "直播房间（非文本聊天频道）"
				item.ChannelName = info.OwnerName + "的直播"
				if info.ChannelName != "" && info.ChannelName != "直播" {
					// the fix in api.go already renamed it; trust that as display name
					item.ChannelName = info.ChannelName
				}
				results = append(results, item)
				continue
			}
			if item.ChannelName == "" {
				item.ChannelName = server.ServerName
			}
			msgs, msgErr := client.GetMessages(info, 3)
			if msgErr != nil {
				item.Status = "open"
				item.Note = "房间可访问，消息探测失败"
			} else if len(msgs) == 0 {
				item.Status = "open"
				item.Note = "房间可访问，近期暂无房主消息"
			} else {
				item.Status = "open"
				newest := msgs[0]
				for _, m := range msgs[1:] {
					if m != nil && m.Time > newest.Time {
						newest = m
					}
				}
				if newest != nil && newest.Time > 0 {
					ts := newest.Time
					if ts > 1e12 {
						ts = ts / 1000
					}
					t := time.Unix(ts, 0)
					item.LastMsgAt = t.Format("2006-01-02 15:04")
					item.LastMsgAgo = humanAge(time.Since(t))
					item.Note = "近期有消息（" + item.LastMsgAgo + "前）"
				} else {
					item.Note = "房间可访问，已有消息记录"
				}
			}
			results = append(results, item)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		rank := func(s pocketRoomSearchResult) int {
			switch {
			case s.Status == "open" && s.LastMsgAt != "":
				return 0
			case s.Status == "open":
				return 1
			case s.Status == "unknown":
				return 2
			default:
				return 3
			}
		}
		ri, rj := rank(results[i]), rank(results[j])
		if ri != rj {
			return ri < rj
		}
		return results[i].RoomID < results[j].RoomID
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"query":   q,
		"results": results,
		"note":    fmt.Sprintf("共返回 %d 个房间候选（服务端最多探测 %d 个搜索结果）", len(results), maxServers),
	})
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "不到1分钟"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "分钟"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "小时"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "天"
	}
}

// enrichPocketRoomName fills channel name for existing subscriptions.
func enrichPocketRoomName(client *pocket48.Client, roomID int64) string {
	if client == nil || roomID <= 0 {
		return ""
	}
	info, err := client.GetRoomInfoByChannelID(roomID)
	if err != nil || info == nil {
		return ""
	}
	if info.ChannelName != "" && info.ChannelName != "直播" {
		return info.ChannelName
	}
	if info.OwnerName != "" {
		return info.OwnerName + "的房间"
	}
	return ""
}
