package logic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
	"pocket48-bot/internal/storage"

	"github.com/gorilla/websocket"
)

// NimDanmakuBridge owns the local Node.js bridge. One sidecar multiplexes all
// live chatrooms and the authenticated QChat room-message connection.
type NimDanmakuBridge struct {
	cfg *config.Config

	mu                sync.RWMutex
	writeMu           sync.Mutex
	cmd               *exec.Cmd
	conn              *websocket.Conn
	started           bool
	stopping          bool
	liveBindings      map[int64]int64 // NIM chatroom id -> Pocket48 channel id
	connectedLives    map[int64]bool
	realtimeRooms     map[int64]int64 // QChat channel id -> Pocket48 channel id
	qchatConnected    bool
	lastRealtimeMsgAt map[int64]time.Time // per-room last realtime message time (UTC)
	stopCh            chan struct{}
	stopOnce          sync.Once
	wg                sync.WaitGroup

	onDanmaku    func(roomID int64, d *DanmakuMessage)
	onGift       func(roomID int64, g *GiftMessage)
	onMember     func(roomID int64, m *MemberEvent)
	onLiveUpdate func(roomID int64, update *LiveUpdate)
	onLiveEnded  func(roomID int64, ended *LiveEnded)
	onRoom       func(pocketRoomID int64, m *RoomRealtimeMessage)
	onConnected  func(roomID int64)
	onError      func(err error)
}

type DanmakuMessage struct {
	Type   string `json:"type"`
	Nick   string `json:"nick"`
	From   string `json:"from"`
	Text   string `json:"text"`
	Avatar string `json:"avatar,omitempty"`
	Time   int64  `json:"time"`
}

type GiftMessage struct {
	Nick     string          `json:"nick"`
	From     string          `json:"from"`
	GiftName string          `json:"giftName"`
	GiftNum  int             `json:"giftNum"`
	GiftID   string          `json:"giftId"`
	Receiver string          `json:"receiver,omitempty"`
	Avatar   string          `json:"avatar,omitempty"`
	Time     int64           `json:"time"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

type LiveUpdate struct {
	OnlineNum int64 `json:"onlineNum"`
	Time      int64 `json:"time"`
}

type LiveEnded struct {
	OnlineNum int64  `json:"onlineNum"`
	Time      int64  `json:"time"`
	Reason    string `json:"reason"`
}

type MemberEvent struct {
	Event  string `json:"event"`
	UserID string `json:"userId"`
	Nick   string `json:"nick"`
	Time   int64  `json:"time"`
}

// RoomRealtimeMessage is the stable wire representation emitted by the
// sidecar for a QChat message.
type RoomRealtimeMessage struct {
	ServerID  int64           `json:"serverId"`
	ChannelID int64           `json:"channelId"`
	From      string          `json:"from,omitempty"`
	FromNick  string          `json:"fromNick,omitempty"`
	Type      string          `json:"type"`
	Body      string          `json:"body,omitempty"`
	Attach    json.RawMessage `json:"attach,omitempty"`
	Ext       json.RawMessage `json:"ext,omitempty"`
	Time      int64           `json:"time"`
	IDServer  string          `json:"idServer,omitempty"`
	IDClient  string          `json:"idClient,omitempty"`
}

type RoomSubscription struct {
	ServerID     int64 `json:"serverId"`
	ChannelID    int64 `json:"channelId"`
	PocketRoomID int64 `json:"pocketRoomId"`
}

type sidecarEvent struct {
	Type           string          `json:"type"`
	Data           json.RawMessage `json:"data,omitempty"`
	Msg            string          `json:"msg,omitempty"`
	RoomID         int64           `json:"roomId,omitempty"`    // Pocket48 channel id
	NIMRoomID      int64           `json:"nimRoomId,omitempty"` // live chatroom id
	ChannelID      int64           `json:"channelId,omitempty"`
	Code           int             `json:"code,omitempty"`
	QChatConnected bool            `json:"qchatConnected,omitempty"`
	LiveConnected  int             `json:"liveConnected,omitempty"`
	LiveConfigured int             `json:"liveConfigured,omitempty"`
}

type sidecarCommand struct {
	Cmd       string             `json:"cmd"`
	RoomID    int64              `json:"roomId,omitempty"`
	NIMRoomID int64              `json:"nimRoomId,omitempty"`
	LiveID    string             `json:"liveId,omitempty"`
	Account   string             `json:"account,omitempty"`
	Token     string             `json:"token,omitempty"`
	Rooms     []RoomSubscription `json:"rooms,omitempty"`
}

func NewNimDanmakuBridge(cfg *config.Config) *NimDanmakuBridge {
	return &NimDanmakuBridge{
		cfg:               cfg,
		liveBindings:      make(map[int64]int64),
		connectedLives:    make(map[int64]bool),
		realtimeRooms:     make(map[int64]int64),
		lastRealtimeMsgAt: make(map[int64]time.Time),
		stopCh:            make(chan struct{}),
	}
}

func (b *NimDanmakuBridge) SetCallbacks(
	onDanmaku func(roomID int64, d *DanmakuMessage),
	onGift func(roomID int64, g *GiftMessage),
	onMember func(roomID int64, m *MemberEvent),
	onLiveUpdate func(roomID int64, update *LiveUpdate),
	onLiveEnded func(roomID int64, ended *LiveEnded),
	onRoom func(pocketRoomID int64, m *RoomRealtimeMessage),
	onConnected func(roomID int64),
	onError func(err error),
) {
	b.onDanmaku = onDanmaku
	b.onGift = onGift
	b.onMember = onMember
	b.onLiveUpdate = onLiveUpdate
	b.onLiveEnded = onLiveEnded
	b.onRoom = onRoom
	b.onConnected = onConnected
	b.onError = onError
}

// parseSidecarCommand accepts both a script path and a complete command line.
// It deliberately does not invoke a shell.
func parseSidecarCommand(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		line = "./sidecar/nim-bridge/index.mjs"
	}

	var args []string
	var current strings.Builder
	var quote rune
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
			current.Reset()
		}
	}
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote == 0 && unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("NIM_SIDECAR_CMD contains an unclosed quote")
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("NIM_SIDECAR_CMD is empty")
	}
	if strings.HasSuffix(strings.ToLower(args[0]), ".mjs") || strings.HasSuffix(strings.ToLower(args[0]), ".js") {
		args = append([]string{"node"}, args...)
	}
	return args, nil
}

func (b *NimDanmakuBridge) Start() error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return fmt.Errorf("bridge already started")
	}
	b.started = true
	b.mu.Unlock()

	parts, err := parseSidecarCommand(b.cfg.NIMSidecarCmd)
	if err != nil {
		b.resetStarted()
		return err
	}
	args := append([]string{}, parts[1:]...)
	hasPort := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "--wsPort=") {
			hasPort = true
			break
		}
	}
	if !hasPort {
		args = append(args, "--wsPort=0")
	}

	cmd := exec.Command(parts[0], args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		b.resetStarted()
		return fmt.Errorf("sidecar stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		b.resetStarted()
		return fmt.Errorf("sidecar stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		b.resetStarted()
		return fmt.Errorf("start NIM sidecar: %w", err)
	}
	b.mu.Lock()
	b.cmd = cmd
	b.mu.Unlock()

	go scanSidecarLogs(stderr, "[NIM-bridge]")
	portCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		reported := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !reported && strings.HasPrefix(line, "PORT:") {
				port, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PORT:")))
				if parseErr != nil {
					errCh <- fmt.Errorf("parse sidecar port %q: %w", line, parseErr)
					return
				}
				reported = true
				portCh <- port
				continue
			}
			log.Printf("[NIM-bridge:stdout] %s", line)
		}
		if !reported {
			if scanErr := scanner.Err(); scanErr != nil {
				errCh <- scanErr
			} else {
				errCh <- fmt.Errorf("sidecar exited before reporting port")
			}
		}
	}()

	var port int
	select {
	case port = <-portCh:
	case startErr := <-errCh:
		_ = cmd.Process.Kill()
		b.resetStarted()
		return fmt.Errorf("sidecar startup: %w", startErr)
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		b.resetStarted()
		return fmt.Errorf("sidecar startup timeout (15s)")
	}

	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		_ = cmd.Process.Kill()
		b.resetStarted()
		return fmt.Errorf("connect to NIM sidecar websocket: %w", err)
	}
	b.mu.Lock()
	b.conn = conn
	b.mu.Unlock()

	b.wg.Add(1)
	go b.readLoop()
	log.Printf("[NIM] sidecar started pid=%d ws=127.0.0.1:%d", cmd.Process.Pid, port)
	return nil
}

func scanSidecarLogs(r interface{ Read([]byte) (int, error) }, prefix string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		var evt sidecarEvent
		if json.Unmarshal([]byte(line), &evt) == nil && evt.Type == "log" {
			log.Printf("%s %s", prefix, evt.Msg)
		} else {
			log.Printf("%s %s", prefix, line)
		}
	}
}

func (b *NimDanmakuBridge) resetStarted() {
	b.mu.Lock()
	b.started = false
	b.cmd = nil
	b.mu.Unlock()
}

func (b *NimDanmakuBridge) Stop() {
	b.stopOnce.Do(func() {
		b.mu.Lock()
		b.stopping = true
		conn := b.conn
		cmd := b.cmd
		b.conn = nil
		b.qchatConnected = false
		b.mu.Unlock()

		if conn != nil {
			payload, _ := json.Marshal(sidecarCommand{Cmd: "shutdown"})
			b.writeMu.Lock()
			_ = conn.WriteMessage(websocket.TextMessage, payload)
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
			b.writeMu.Unlock()
			_ = conn.Close()
		}
		close(b.stopCh)

		if cmd != nil && cmd.Process != nil {
			done := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		}
		b.wg.Wait()
		log.Printf("[NIM] sidecar stopped")
	})
}

func (b *NimDanmakuBridge) send(command sidecarCommand) error {
	b.mu.RLock()
	conn := b.conn
	stopping := b.stopping
	b.mu.RUnlock()
	if conn == nil || stopping {
		return fmt.Errorf("NIM sidecar is not connected")
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (b *NimDanmakuBridge) ConnectRoom(pocketRoomID, nimRoomID int64, liveID string) error {
	if pocketRoomID == 0 || nimRoomID == 0 {
		return fmt.Errorf("pocket room id and NIM room id are required")
	}
	b.mu.Lock()
	b.liveBindings[nimRoomID] = pocketRoomID
	b.mu.Unlock()
	return b.send(sidecarCommand{
		Cmd:       "connect_live",
		RoomID:    pocketRoomID,
		NIMRoomID: nimRoomID,
		LiveID:    liveID,
		Account:   b.cfg.NIMAccount,
		Token:     b.cfg.NIMToken,
	})
}

func (b *NimDanmakuBridge) DisconnectRoom(nimRoomID int64) error {
	b.mu.Lock()
	delete(b.liveBindings, nimRoomID)
	delete(b.connectedLives, nimRoomID)
	b.mu.Unlock()
	return b.send(sidecarCommand{Cmd: "disconnect_live", NIMRoomID: nimRoomID})
}

func (b *NimDanmakuBridge) SyncRooms(rooms []RoomSubscription) error {
	if strings.TrimSpace(b.cfg.NIMAccount) == "" || strings.TrimSpace(b.cfg.NIMToken) == "" {
		return fmt.Errorf("NIM_ACCOUNT and NIM_TOKEN are required for room realtime monitoring")
	}
	next := make(map[int64]int64, len(rooms))
	for _, room := range rooms {
		next[room.ChannelID] = room.PocketRoomID
	}
	b.mu.Lock()
	b.realtimeRooms = next
	b.mu.Unlock()
	return b.send(sidecarCommand{Cmd: "sync_rooms", Account: b.cfg.NIMAccount, Token: b.cfg.NIMToken, Rooms: rooms})
}

func (b *NimDanmakuBridge) RoomRealtimeAvailable(pocketRoomID int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.qchatConnected {
		return false
	}
	for _, mappedRoomID := range b.realtimeRooms {
		if mappedRoomID == pocketRoomID {
			return true
		}
	}
	return false
}

// RoomRealtimeActive returns true only if QChat is connected AND we have
// received a realtime message for this room within the last 5 minutes.
// Unlike RoomRealtimeAvailable which only checks the connection state,
// this ensures we skip polling only when QChat is actually delivering messages.
func (b *NimDanmakuBridge) RoomRealtimeActive(pocketRoomID int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.qchatConnected {
		return false
	}
	lastAt, ok := b.lastRealtimeMsgAt[pocketRoomID]
	if !ok {
		return false
	}
	return time.Since(lastAt) < 5*time.Minute
}

func (b *NimDanmakuBridge) HasLiveBinding(pocketRoomID, nimRoomID int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.liveBindings[nimRoomID] == pocketRoomID
}

func eventPocketRoomID(evt sidecarEvent) int64 { return evt.RoomID }

func (b *NimDanmakuBridge) readLoop() {
	defer b.wg.Done()
	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		b.mu.RLock()
		conn := b.conn
		b.mu.RUnlock()
		if conn == nil {
			return
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			b.mu.Lock()
			b.qchatConnected = false
			stopping := b.stopping
			b.mu.Unlock()
			if !stopping && websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				b.reportError(fmt.Errorf("sidecar websocket read: %w", err))
			}
			return
		}

		var evt sidecarEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			log.Printf("[NIM] invalid sidecar event: %v raw=%s", err, string(raw))
			continue
		}
		roomID := eventPocketRoomID(evt)
		switch evt.Type {
		case "live_connected":
			b.mu.Lock()
			b.connectedLives[evt.NIMRoomID] = true
			b.mu.Unlock()
			if b.onConnected != nil {
				b.onConnected(roomID)
			}
			log.Printf("[NIM-live] connected room=%d nimRoom=%d", roomID, evt.NIMRoomID)
		case "live_disconnected":
			b.mu.Lock()
			delete(b.connectedLives, evt.NIMRoomID)
			b.mu.Unlock()
			log.Printf("[NIM-live] disconnected room=%d nimRoom=%d", roomID, evt.NIMRoomID)
		case "qchat_connected":
			b.mu.Lock()
			b.qchatConnected = true
			b.mu.Unlock()
			log.Printf("[NIM-room] QChat connected")
		case "qchat_disconnected":
			b.mu.Lock()
			b.qchatConnected = false
			b.mu.Unlock()
			log.Printf("[NIM-room] QChat disconnected: %s", evt.Msg)
		case "nim_status":
			b.mu.Lock()
			b.qchatConnected = evt.QChatConnected
			b.mu.Unlock()
			qchatStatus := "disconnected"
			if evt.QChatConnected {
				qchatStatus = "connected"
			}
			log.Printf("[NIM-health] qchat=%s", qchatStatus)
			liveStatus := "idle"
			if evt.LiveConfigured > 0 && evt.LiveConnected == evt.LiveConfigured {
				liveStatus = "connected"
			} else if evt.LiveConfigured > 0 {
				liveStatus = "reconnecting"
			}
			log.Printf("[NIM-live-health] status=%s connected=%d configured=%d", liveStatus, evt.LiveConnected, evt.LiveConfigured)
		case "danmaku":
			var msg DanmakuMessage
			if json.Unmarshal(evt.Data, &msg) == nil && b.onDanmaku != nil {
				b.onDanmaku(roomID, &msg)
			}
		case "gift":
			var msg GiftMessage
			if json.Unmarshal(evt.Data, &msg) == nil && b.onGift != nil {
				b.onGift(roomID, &msg)
			}
		case "live_update":
			var update LiveUpdate
			if json.Unmarshal(evt.Data, &update) == nil && b.onLiveUpdate != nil {
				b.onLiveUpdate(roomID, &update)
			}
		case "live_ended":
			var ended LiveEnded
			if json.Unmarshal(evt.Data, &ended) == nil && b.onLiveEnded != nil {
				b.onLiveEnded(roomID, &ended)
			}
			b.mu.Lock()
			delete(b.liveBindings, evt.NIMRoomID)
			delete(b.connectedLives, evt.NIMRoomID)
			b.mu.Unlock()
		case "member_event":
			var msg MemberEvent
			if json.Unmarshal(evt.Data, &msg) == nil && b.onMember != nil {
				b.onMember(roomID, &msg)
			}
		case "room_message":
			var msg RoomRealtimeMessage
			if err := json.Unmarshal(evt.Data, &msg); err != nil {
				b.reportError(fmt.Errorf("decode QChat room message: %w", err))
				continue
			}
			if b.onRoom != nil {
				b.onRoom(evt.RoomID, &msg)
			}
		case "error":
			b.reportError(fmt.Errorf("sidecar error: %s (code=%d)", evt.Msg, evt.Code))
		case "log":
			log.Printf("[NIM] %s", evt.Msg)
		}
	}
}

func (b *NimDanmakuBridge) reportError(err error) {
	log.Printf("[NIM] %v", err)
	if b.onError != nil {
		b.onError(err)
	}
}

func decodeRawObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var encoded string
	if raw[0] == '"' && json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m RoomRealtimeMessage) toPocketMessage(room *pocket48.RoomInfo) (*pocket48.Message, error) {
	if room == nil {
		room = &pocket48.RoomInfo{ServerID: m.ServerID, ChannelID: m.ChannelID}
	}
	msgType := strings.ToUpper(strings.TrimSpace(m.Type))
	body := m.Body
	switch msgType {
	case "0":
		msgType = string(pocket48.MsgText)
	case "1":
		msgType = string(pocket48.MsgImage)
	case "2":
		msgType = string(pocket48.MsgAudio)
	case "3":
		msgType = string(pocket48.MsgVideo)
	}
	if msgType == string(pocket48.MsgImage) && hasQChatMediaURL(string(m.Attach), []string{"url", "image", "img", "pic", "path", "originUrl", "sourceUrl", "thumbUrl"}) {
		body = string(m.Attach)
	} else if msgType == string(pocket48.MsgAudio) && hasQChatMediaURL(string(m.Attach), []string{"url", "audio", "voice", "path", "originUrl", "sourceUrl"}) {
		body = string(m.Attach)
	} else if msgType == string(pocket48.MsgVideo) && hasQChatMediaURL(string(m.Attach), []string{"url", "video", "videoUrl", "path", "originUrl", "sourceUrl", "playUrl"}) {
		body = string(m.Attach)
	}
	if strings.EqualFold(m.Type, "custom") {
		attach, err := decodeRawObject(m.Attach)
		if err != nil {
			return nil, fmt.Errorf("decode room message attach: %w", err)
		}
		if rawType := attach["messageType"]; len(rawType) > 0 {
			_ = json.Unmarshal(rawType, &msgType)
		}
		// Preserve the complete structured payload. Downstream parsers need fields
		// such as flipCardInfo, replyInfo and media URLs.
		if len(m.Attach) > 0 {
			body = string(m.Attach)
		}
	}

	msg := &pocket48.Message{
		Room:        room,
		MsgIDServer: m.IDServer,
		MsgIDClient: m.IDClient,
		Type:        pocket48.MessageType(strings.ToUpper(msgType)),
		Body:        body,
		Time:        normalizeMessageTimestampMs(m.Time),
		RawExt:      string(m.Ext),
		DirectMedia: true,
	}
	if len(m.Ext) > 0 {
		var ext pocket48.ExtInfo
		raw := m.Ext
		var encoded string
		if raw[0] == '"' && json.Unmarshal(raw, &encoded) == nil {
			raw = json.RawMessage(encoded)
		}
		if err := json.Unmarshal(raw, &ext); err == nil {
			msg.ExtInfo = ext
			msg.NickName = ext.User.Nickname
		}
	}
	if msg.ExtInfo.User.UserID == 0 {
		if userID, err := strconv.ParseInt(strings.TrimSpace(m.From), 10, 64); err == nil {
			msg.ExtInfo.User.UserID = userID
		}
	}
	if msg.NickName == "" {
		msg.NickName = strings.TrimSpace(m.FromNick)
		msg.ExtInfo.User.Nickname = msg.NickName
	}
	return msg, nil
}

func (b *Bot) startNIMBridge() {
	b.refreshNIMCredentials()
	b.nimDanmaku.SetCallbacks(
		b.handleDanmakuMessage,
		b.handleDanmakuGift,
		b.handleMemberEvent,
		b.handleLiveUpdate,
		b.handleLiveEnded,
		b.handleRoomRealtimeMessage,
		b.handleDanmakuConnected,
		b.handleDanmakuError,
	)
	if err := b.nimDanmaku.Start(); err != nil {
		log.Printf("[NIM] failed to start bridge: %v", err)
		return
	}
	if b.cfg.NIMRoomMessageEnabled {
		b.refreshNIMRoomSubscriptions()
		go b.runNIMRoomSyncLoop()
	}
	if b.cfg.NIMEnabled {
		b.refreshNIMLiveRooms()
		go b.runNIMLiveDiscoveryLoop()
	}
}

func (b *Bot) tryPocketPasswordLogin() bool {
	mobile := strings.TrimSpace(b.cfg.PocketUsername)
	password := b.cfg.PocketPassword
	if mobile == "" || password == "" {
		return false
	}
	b.LogInfo("Trying Pocket48 password login for %s...", maskMobile(mobile))
	if err := b.pocket.LoginWithPassword(mobile, password); err != nil {
		log.Printf("[Pocket48] password login failed: %v", err)
		return false
	}
	b.clearPocketAuthExpired()
	b.refreshNIMCredentials()
	b.LogInfo("Pocket48 password login successful")
	return true
}

// refreshNIMCredentials obtains the account-scoped NIM token from Pocket48.
// Existing configured credentials remain usable if the refresh is temporarily
// unavailable, which keeps restarts resilient during an API outage.
func (b *Bot) refreshNIMCredentials() bool {
	if strings.TrimSpace(b.cfg.PocketToken) == "" {
		return strings.TrimSpace(b.cfg.NIMAccount) != "" && strings.TrimSpace(b.cfg.NIMToken) != ""
	}
	info, err := b.pocket.GetIMUserInfo()
	if err != nil {
		log.Printf("[NIM] unable to refresh credentials: %v", err)
		return strings.TrimSpace(b.cfg.NIMAccount) != "" && strings.TrimSpace(b.cfg.NIMToken) != ""
	}
	if info.AccID == b.cfg.NIMAccount && info.Pwd == b.cfg.NIMToken {
		return true
	}
	if err := b.cfg.UpdateNIMCredentials(info.AccID, info.Pwd); err != nil {
		log.Printf("[NIM] save refreshed credentials: %v", err)
	}
	log.Printf("[NIM] credentials refreshed for account %s", info.AccID)
	return true
}

func (b *Bot) runNIMRoomSyncLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		b.refreshNIMRoomSubscriptions()
	}
}

func (b *Bot) refreshNIMRoomSubscriptions() {
	roomIDs := make(map[int64]struct{})
	for _, rooms := range b.cfg.GroupSubscriptions {
		for _, roomID := range rooms {
			roomIDs[roomID] = struct{}{}
		}
	}
	subscriptions := make([]RoomSubscription, 0, len(roomIDs))
	for roomID := range roomIDs {
		info, err := b.getCachedRoomInfo(roomID)
		if err != nil || info == nil {
			log.Printf("[NIM-room] cannot resolve room %d for subscription: %v", roomID, err)
			continue
		}
		log.Printf("[NIM-room] resolved room %d: serverId=%d channelId=%d owner=%s", roomID, info.ServerID, info.ChannelID, info.OwnerName)
		subscriptions = append(subscriptions, RoomSubscription{
			ServerID: info.ServerID, ChannelID: info.ChannelID, PocketRoomID: roomID,
		})
	}
	if err := b.nimDanmaku.SyncRooms(subscriptions); err != nil {
		log.Printf("[NIM-room] subscription sync failed: %v", err)
	}
}

func (b *Bot) runNIMLiveDiscoveryLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		b.refreshNIMLiveRooms()
	}
}

// refreshNIMLiveRooms makes startup during an already-running live stream work;
// relying only on LIVEPUSH would miss streams whose push predates bot startup.
func (b *Bot) refreshNIMLiveRooms() {
	lives, err := b.pocket.GetLiveList()
	if err != nil {
		log.Printf("[NIM-live] active live discovery failed: %v", err)
		return
	}
	roomIDs := make(map[int64]struct{})
	for _, rooms := range b.cfg.GroupSubscriptions {
		for _, roomID := range rooms {
			roomIDs[roomID] = struct{}{}
		}
	}
	activeLiveIDs := make(map[string]struct{})
	for _, live := range lives {
		if live.LiveID != "" {
			activeLiveIDs[live.LiveID] = struct{}{}
		}
	}
	for roomID := range roomIDs {
		room, roomErr := b.getCachedRoomInfo(roomID)
		if roomErr != nil || room == nil {
			continue
		}
		for _, live := range lives {
			ownerMatches := live.UserID == room.OwnerID || live.MemberID == room.OwnerID
			nameMatches := room.OwnerName != "" && (live.MemberName == room.OwnerName || live.NickName == room.OwnerName)
			if !ownerMatches && !nameMatches {
				continue
			}
			// getLiveList discovers the active liveId. Its roomId is not the
			// authoritative NIM chatroom id; getLiveOne supplies that value.
			b.connectDanmakuForLive(live.LiveID, 0, room)
			break
		}
	}
	b.finishMissingLiveSessions(activeLiveIDs)
}

func resolveNIMLiveRoomID(liveID string, fallback int64, getLiveOne func(string) (*pocket48.LiveOne, error)) (int64, error) {
	if liveID == "" {
		return fallback, nil
	}
	liveOne, err := getLiveOne(liveID)
	if err != nil {
		if fallback != 0 {
			return fallback, nil
		}
		return 0, err
	}
	if liveOne == nil || liveOne.RoomID == 0 {
		if fallback != 0 {
			return fallback, nil
		}
		return 0, fmt.Errorf("getLiveOne returned no NIM chatroom id for live %s", liveID)
	}
	return liveOne.RoomID, nil
}

func (b *Bot) connectDanmakuForLive(liveID string, nimRoomID int64, room *pocket48.RoomInfo) {
	if room == nil {
		return
	}
	var liveOne *pocket48.LiveOne
	var liveOneErr error
	if liveID != "" {
		liveOne, liveOneErr = b.pocket.GetLiveOne(liveID)
	}
	resolvedRoomID, err := resolveNIMLiveRoomID(liveID, nimRoomID, func(string) (*pocket48.LiveOne, error) {
		return liveOne, liveOneErr
	})
	if err != nil {
		log.Printf("[NIM-live] GetLiveOne failed for live %s: %v", liveID, err)
		return
	}
	nimRoomID = resolvedRoomID
	if nimRoomID == 0 {
		log.Printf("[NIM-live] no chatroom id for live %s", liveID)
		return
	}
	initialOnline := int64(0)
	if liveOne != nil {
		initialOnline = liveOne.OnlineNum
	}
	if !b.beginLiveSession(room, liveID, nimRoomID, initialOnline) {
		return
	}
	if b.nimDanmaku.HasLiveBinding(room.ChannelID, nimRoomID) {
		return
	}
	if err := b.nimDanmaku.ConnectRoom(room.ChannelID, nimRoomID, liveID); err != nil {
		log.Printf("[NIM-live] connect room failed: %v", err)
	}
}

func (b *Bot) shouldDeferRoomRealtimeToREST(msg *pocket48.Message) bool {
	if msg == nil || !b.cfg.NIMRoomMessagePollFallback {
		return false
	}
	if msg.ExtInfo.User.UserID == 0 {
		return true
	}
	switch msg.Type {
	case pocket48.MsgText:
		return strings.TrimSpace(msg.Body) == ""
	case pocket48.MsgImage, pocket48.MsgExpressImage:
		return !hasQChatMediaURL(msg.Body, []string{"url", "image", "img", "pic", "path", "originUrl", "sourceUrl", "thumbUrl"})
	case pocket48.MsgAudio:
		return !hasQChatMediaURL(msg.Body, []string{"url", "audio", "voice", "path", "originUrl", "sourceUrl"})
	case pocket48.MsgVideo:
		return !hasQChatMediaURL(msg.Body, []string{"url", "video", "videoUrl", "path", "originUrl", "sourceUrl", "playUrl"})
	case pocket48.MsgReply, pocket48.MsgGiftReply, pocket48.MsgGiftText, pocket48.MsgLivePush:
		return strings.TrimSpace(msg.Body) == ""
	case pocket48.MsgAudioGiftReply:
		_, voiceURL, _, ok := parseAudioGiftReplyMessage(msg.Body)
		return !ok || voiceURL == ""
	case pocket48.MsgFlipCard:
		_, _, _, ok := parseFlipCardBody(msg.Body)
		return !ok
	case pocket48.MsgFlipCardAudio:
		_, _, _, ok := parseFlipCardBody(msg.Body)
		return !ok || !hasQChatMediaURL(msg.Body, []string{"url", "audio", "voice", "voiceUrl", "path", "originUrl", "sourceUrl"})
	case pocket48.MsgFlipCardVideo:
		_, _, _, ok := parseFlipCardBody(msg.Body)
		return !ok || !hasQChatMediaURL(msg.Body, []string{"url", "video", "videoUrl", "path", "originUrl", "sourceUrl", "playUrl"})
	default:
		return true
	}
}

func hasQChatMediaURL(body string, keys []string) bool {
	body = strings.TrimSpace(body)
	if body == "" || body == "null" {
		return false
	}
	if strings.HasPrefix(body, "http://") || strings.HasPrefix(body, "https://") || strings.HasPrefix(body, "//") || strings.HasPrefix(body, "/") {
		return true
	}
	if !strings.HasPrefix(body, "{") && !strings.HasPrefix(body, "[") {
		return false
	}
	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return false
	}
	return strings.TrimSpace(findStringField(payload, keys)) != ""
}

func (b *Bot) enqueueRoomRealtimeTask(roomID int64, prepare, send func()) {
	b.roomRealtimeOrderMu.Lock()
	if b.roomRealtimeTails == nil {
		b.roomRealtimeTails = make(map[int64]chan struct{})
	}
	previous := b.roomRealtimeTails[roomID]
	done := make(chan struct{})
	b.roomRealtimeTails[roomID] = done
	b.roomRealtimeOrderMu.Unlock()

	b.roomMediaWG.Add(1)
	go func() {
		defer b.roomMediaWG.Done()
		defer func() {
			close(done)
			b.roomRealtimeOrderMu.Lock()
			if b.roomRealtimeTails[roomID] == done {
				delete(b.roomRealtimeTails, roomID)
			}
			b.roomRealtimeOrderMu.Unlock()
		}()
		if prepare != nil {
			prepare()
		}
		if previous != nil {
			<-previous
		}
		if send != nil {
			send()
		}
	}()
}

func (b *Bot) processRoomRealtimeMessage(msg *pocket48.Message) {
	if msg == nil || !b.markMessageSeen(msg) {
		return
	}
	roomID := int64(0)
	if msg.Room != nil {
		roomID = msg.Room.ChannelID
	}
	targetGroups := b.getTargetGroupsForRoom(roomID)
	if len(targetGroups) == 0 {
		return
	}
	started := time.Now()
	b.enqueueRoomRealtimeTask(roomID, nil, func() {
		b.processSinglePocketMessage(msg, targetGroups)
		log.Printf("[NIM-room] ordered message processed room=%d id=%s type=%s elapsed=%s", roomID, msg.MsgIDServer, msg.Type, time.Since(started).Round(time.Millisecond))
	})
}

func qchatIdentityMessageKey(roomID int64, serverID, clientID string) string {
	id := strings.TrimSpace(serverID)
	if id == "" {
		id = strings.TrimSpace(clientID)
	}
	if roomID == 0 || id == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", roomID, id)
}

func (b *Bot) loadQChatOwnerIdentity(roomID int64) {
	b.mu.Lock()
	if b.qchatIdentityLoaded == nil {
		b.qchatIdentityLoaded = make(map[int64]bool)
	}
	if b.qchatIdentityLoaded[roomID] {
		b.mu.Unlock()
		return
	}
	b.qchatIdentityLoaded[roomID] = true
	b.mu.Unlock()

	if b.storage == nil {
		return
	}
	identity, err := b.storage.GetQChatIdentity(roomID)
	if err != nil {
		log.Printf("[NIM-room] load QChat identity room=%d: %v", roomID, err)
		return
	}
	if identity == nil || strings.TrimSpace(identity.Account) == "" || identity.UserID == 0 {
		return
	}
	b.mu.Lock()
	if b.qchatOwnerIdentities == nil {
		b.qchatOwnerIdentities = make(map[int64]storage.QChatIdentity)
	}
	b.qchatOwnerIdentities[roomID] = *identity
	b.mu.Unlock()
	log.Printf("[NIM-room] loaded owner identity room=%d user=%d account=%q", roomID, identity.UserID, identity.Account)
}

func (b *Bot) saveQChatOwnerIdentity(roomID int64, identity storage.QChatIdentity) {
	if b.storage == nil {
		return
	}
	if err := b.storage.SaveQChatIdentity(roomID, identity); err != nil {
		log.Printf("[NIM-room] save QChat identity room=%d: %v", roomID, err)
	}
}

func (b *Bot) resolveQChatOwnerIdentity(room *pocket48.RoomInfo, raw *RoomRealtimeMessage, msg *pocket48.Message) bool {
	if room == nil || raw == nil || msg == nil || msg.ExtInfo.User.UserID != 0 {
		return msg != nil && msg.ExtInfo.User.UserID != 0
	}
	account := strings.TrimSpace(raw.From)
	if account == "" {
		return false
	}
	b.loadQChatOwnerIdentity(room.ChannelID)
	key := qchatIdentityMessageKey(room.ChannelID, raw.IDServer, raw.IDClient)
	now := time.Now()
	var learned *storage.QChatIdentity
	newlyLearned := false

	b.mu.Lock()
	if b.qchatOwnerIdentities == nil {
		b.qchatOwnerIdentities = make(map[int64]storage.QChatIdentity)
	}
	if b.qchatPendingIdentities == nil {
		b.qchatPendingIdentities = make(map[string]qchatPendingIdentity)
	}
	if b.qchatRESTIdentities == nil {
		b.qchatRESTIdentities = make(map[string]qchatRESTIdentity)
	}
	identity, known := b.qchatOwnerIdentities[room.ChannelID]
	if known && identity.UserID == room.OwnerID && identity.Account == account {
		learned = &identity
	} else if rest, ok := b.qchatRESTIdentities[key]; ok && rest.UserID == room.OwnerID {
		identity = storage.QChatIdentity{Account: account, UserID: rest.UserID, Nickname: rest.Nickname, UpdatedAt: now.Unix()}
		b.qchatOwnerIdentities[room.ChannelID] = identity
		delete(b.qchatRESTIdentities, key)
		learned = &identity
		newlyLearned = true
	} else if key != "" {
		b.qchatPendingIdentities[key] = qchatPendingIdentity{Account: account, SeenAt: now}
	}
	b.pruneQChatIdentityCachesLocked(now)
	b.mu.Unlock()

	if learned == nil {
		return false
	}
	msg.ExtInfo.User.UserID = learned.UserID
	msg.ExtInfo.ChannelRole = "2"
	if msg.NickName == "" {
		msg.NickName = learned.Nickname
		if msg.NickName == "" {
			msg.NickName = room.OwnerName
		}
		msg.ExtInfo.User.Nickname = msg.NickName
	}
	if newlyLearned {
		b.saveQChatOwnerIdentity(room.ChannelID, *learned)
		log.Printf("[NIM-room] learned owner identity room=%d user=%d account=%q", room.ChannelID, learned.UserID, learned.Account)
	}
	return true
}

func (b *Bot) observeRESTQChatIdentities(room *pocket48.RoomInfo, msgs []*pocket48.Message) {
	if room == nil || room.OwnerID == 0 {
		return
	}
	now := time.Now()
	var learned []storage.QChatIdentity
	b.mu.Lock()
	if b.qchatOwnerIdentities == nil {
		b.qchatOwnerIdentities = make(map[int64]storage.QChatIdentity)
	}
	if b.qchatPendingIdentities == nil {
		b.qchatPendingIdentities = make(map[string]qchatPendingIdentity)
	}
	if b.qchatRESTIdentities == nil {
		b.qchatRESTIdentities = make(map[string]qchatRESTIdentity)
	}
	for _, msg := range msgs {
		if msg == nil || msg.ExtInfo.User.UserID != room.OwnerID {
			continue
		}
		key := qchatIdentityMessageKey(room.ChannelID, msg.MsgIDServer, msg.MsgIDClient)
		if key == "" {
			continue
		}
		rest := qchatRESTIdentity{UserID: msg.ExtInfo.User.UserID, Nickname: msg.NickName, SeenAt: now}
		b.qchatRESTIdentities[key] = rest
		if pending, ok := b.qchatPendingIdentities[key]; ok && pending.Account != "" {
			identity := storage.QChatIdentity{Account: pending.Account, UserID: rest.UserID, Nickname: rest.Nickname, UpdatedAt: now.Unix()}
			b.qchatOwnerIdentities[room.ChannelID] = identity
			delete(b.qchatPendingIdentities, key)
			delete(b.qchatRESTIdentities, key)
			learned = append(learned, identity)
		}
	}
	b.pruneQChatIdentityCachesLocked(now)
	b.mu.Unlock()
	for _, identity := range learned {
		b.saveQChatOwnerIdentity(room.ChannelID, identity)
		log.Printf("[NIM-room] learned owner identity room=%d user=%d account=%q", room.ChannelID, identity.UserID, identity.Account)
	}
}

func (b *Bot) pruneQChatIdentityCachesLocked(now time.Time) {
	cutoff := now.Add(-10 * time.Minute)
	for key, pending := range b.qchatPendingIdentities {
		if pending.SeenAt.Before(cutoff) {
			delete(b.qchatPendingIdentities, key)
		}
	}
	for key, recent := range b.qchatRESTIdentities {
		if recent.SeenAt.Before(cutoff) {
			delete(b.qchatRESTIdentities, key)
		}
	}
}

func (b *Bot) handleRoomRealtimeMessage(pocketRoomID int64, raw *RoomRealtimeMessage) {
	if raw == nil || raw.ChannelID == 0 || !b.cfg.NIMRoomMessageEnabled {
		return
	}
	if len(b.getTargetGroupsForRoom(pocketRoomID)) == 0 {
		return
	}
	room, err := b.getCachedRoomInfo(pocketRoomID)
	if err != nil {
		log.Printf("[NIM-room] resolve room %d: %v", pocketRoomID, err)
		return
	}
	msg, err := raw.toPocketMessage(room)
	if err != nil {
		log.Printf("[NIM-room] normalize room %d message: %v", pocketRoomID, err)
		return
	}
	b.resolveQChatOwnerIdentity(room, raw, msg)
	log.Printf("[NIM-room] received room=%d channel=%d server=%d id=%s type=%s sender=%d from=%q nick=%q", pocketRoomID, raw.ChannelID, raw.ServerID, msg.MsgIDServer, msg.Type, msg.ExtInfo.User.UserID, raw.From, raw.FromNick)
	if b.shouldDeferRoomRealtimeToREST(msg) {
		log.Printf("[NIM-room] defer incomplete message to REST room=%d id=%s type=%s sender=%d", pocketRoomID, msg.MsgIDServer, msg.Type, msg.ExtInfo.User.UserID)
	} else {
		b.processRoomRealtimeMessage(msg)
	}
	b.nimDanmaku.mu.Lock()
	b.nimDanmaku.lastRealtimeMsgAt[pocketRoomID] = time.Now()
	b.nimDanmaku.mu.Unlock()
}

func (b *Bot) handleDanmakuMessage(roomID int64, d *DanmakuMessage) {
	if d == nil || strings.TrimSpace(d.Text) == "" || !b.cfg.NIMLiveDanmakuEnabled {
		return
	}
	fromID, _ := strconv.ParseInt(d.From, 10, 64)
	if fromID == 0 || !b.isKnownStar(fromID) || fromID == b.getRoomOwnerID(roomID) {
		return
	}
	targetGroups := b.getTargetGroupsForRoom(roomID)
	roomName := b.getRoomNameForDanmaku(roomID)
	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, napcat.TextSegment(fmt.Sprintf("💬 %s直播间 · %s: %s", roomName, d.Nick, d.Text)))
	}
}

func (b *Bot) handleDanmakuGift(roomID int64, g *GiftMessage) {
	if g == nil {
		return
	}
	_, totalLegs, _ := extractChickenLegFromRaw(g.Raw, g.GiftName, int64(g.GiftNum))
	var score float64
	if scoreGift, ok := parseAnnualScoreGiftMessage(string(g.Raw)); ok {
		score = scoreGift.TotalScore
	}
	b.liveSessionsMu.Lock()
	if session := b.liveSessions[roomID]; session != nil && !session.Ended {
		session.ChickenLegs += totalLegs
		session.AnnualScore += score
	}
	b.liveSessionsMu.Unlock()
}

func (b *Bot) beginLiveSession(room *pocket48.RoomInfo, liveID string, nimRoomID, initialOnline int64) bool {
	if room == nil {
		return false
	}
	b.liveSessionsMu.Lock()
	defer b.liveSessionsMu.Unlock()
	current := b.liveSessions[room.ChannelID]
	if current != nil && current.LiveID == liveID {
		if initialOnline > current.PeakOnline {
			current.PeakOnline = initialOnline
		}
		return !current.Ended
	}
	b.liveSessions[room.ChannelID] = &LiveGiftSession{
		LiveID: liveID, LiveRoomID: nimRoomID, LiveOwnerID: room.OwnerID,
		LiveOwnerName: room.OwnerName, StartedAt: time.Now().UnixMilli(), PeakOnline: initialOnline,
	}
	return true
}

func (b *Bot) handleLiveUpdate(roomID int64, update *LiveUpdate) {
	if update == nil || update.OnlineNum < 0 {
		return
	}
	b.liveSessionsMu.Lock()
	if session := b.liveSessions[roomID]; session != nil && !session.Ended && update.OnlineNum > session.PeakOnline {
		session.PeakOnline = update.OnlineNum
	}
	b.liveSessionsMu.Unlock()
}

func (b *Bot) handleLiveEnded(roomID int64, ended *LiveEnded) {
	if ended != nil {
		b.handleLiveUpdate(roomID, &LiveUpdate{OnlineNum: ended.OnlineNum, Time: ended.Time})
	}
	b.finishLiveSession(roomID)
}

func (b *Bot) finishMissingLiveSessions(activeLiveIDs map[string]struct{}) {
	var endedRooms []int64
	b.liveSessionsMu.Lock()
	for roomID, session := range b.liveSessions {
		if session == nil || session.Ended || session.LiveID == "" {
			continue
		}
		if _, active := activeLiveIDs[session.LiveID]; !active {
			endedRooms = append(endedRooms, roomID)
		}
	}
	b.liveSessionsMu.Unlock()
	for _, roomID := range endedRooms {
		b.finishLiveSession(roomID)
	}
}

func (b *Bot) finishLiveSession(roomID int64) {
	b.liveSessionsMu.Lock()
	session := b.liveSessions[roomID]
	if session == nil || session.Ended {
		b.liveSessionsMu.Unlock()
		return
	}
	session.Ended = true
	snapshot := *session
	b.liveSessionsMu.Unlock()

	name := strings.TrimSpace(snapshot.LiveOwnerName)
	if name == "" {
		name = b.getRoomNameForDanmaku(roomID)
	}
	text := fmt.Sprintf("⏹️ %s的直播已结束", name)
	if snapshot.StartedAt > 0 {
		duration := time.Since(time.UnixMilli(snapshot.StartedAt))
		text += "\n直播时长：" + formatDouyinDuration(duration)
	}
	if snapshot.ChickenLegs > 0 {
		text += fmt.Sprintf("\n本场鸡腿值：%d", snapshot.ChickenLegs)
	}
	if snapshot.AnnualScore > 0 {
		text += "\n本场总选记分收入：" + formatScoreValue(snapshot.AnnualScore)
	}
	if snapshot.PeakOnline > 0 {
		text += fmt.Sprintf("\n最高在线人数：%d", snapshot.PeakOnline)
	}
	for _, gid := range b.getTargetGroupsForRoom(roomID) {
		b.napcat.SendGroupMessage(gid, napcat.TextSegment(text))
	}
}

func (b *Bot) handleMemberEvent(roomID int64, m *MemberEvent) {
	if m == nil || !b.cfg.NIMViewerEventEnabled {
		return
	}
	fromID, _ := strconv.ParseInt(m.UserID, 10, 64)
	if fromID == 0 || !b.isKnownStar(fromID) || fromID == b.getRoomOwnerID(roomID) {
		return
	}
	now := time.Now()
	key := fmt.Sprintf("%d:%s", roomID, m.UserID)
	roomName := b.getRoomNameForDanmaku(roomID)
	var text string
	switch m.Event {
	case "memberEnter":
		b.memberEnterMu.Lock()
		b.memberEnterTimes[key] = now
		b.memberEnterMu.Unlock()
		text = fmt.Sprintf("👀 %s进入了%s的直播间\n%s", m.Nick, roomName, now.Format("2006-01-02 15:04:05"))
	case "memberExit":
		var duration string
		b.memberEnterMu.Lock()
		if entered, ok := b.memberEnterTimes[key]; ok {
			duration = fmt.Sprintf("\n观看时长%s", now.Sub(entered).Round(time.Second))
			delete(b.memberEnterTimes, key)
		}
		b.memberEnterMu.Unlock()
		text = fmt.Sprintf("👀 %s离开了%s的直播间%s\n%s", m.Nick, roomName, duration, now.Format("2006-01-02 15:04:05"))
	default:
		return
	}
	for _, gid := range b.getTargetGroupsForRoom(roomID) {
		b.napcat.SendGroupMessage(gid, napcat.TextSegment(text))
	}
}

func (b *Bot) getRoomOwnerID(roomID int64) int64 {
	info, err := b.getCachedRoomInfo(roomID)
	if err == nil && info != nil {
		return info.OwnerID
	}
	return 0
}

func (b *Bot) isKnownStar(userID int64) bool {
	if userID == 0 {
		return false
	}
	b.mu.RLock()
	cached, ok := b.userDetailCache[userID]
	b.mu.RUnlock()
	if ok && time.Now().Before(cached.expiresAt) {
		return cached.info != nil && cached.info.IsStar
	}
	detail, err := b.pocket.GetUserDetailInfo(userID)
	if err != nil {
		log.Printf("[NIM-live] lookup user %d: %v", userID, err)
		return false
	}
	b.mu.Lock()
	b.userDetailCache[userID] = cachedUserDetail{info: detail, expiresAt: time.Now().Add(10 * time.Minute)}
	b.mu.Unlock()
	return detail.IsStar
}

func (b *Bot) handleDanmakuConnected(roomID int64) {
	log.Printf("[NIM-live] connected Pocket48 room=%d", roomID)
}

func (b *Bot) handleDanmakuError(err error) {
	log.Printf("[NIM] %v", err)
}

func (b *Bot) getRoomNameForDanmaku(roomID int64) string {
	info, err := b.getCachedRoomInfo(roomID)
	if err == nil && info != nil {
		if strings.TrimSpace(info.OwnerName) != "" {
			return info.OwnerName
		}
		if strings.TrimSpace(info.ChannelName) != "" {
			return info.ChannelName
		}
	}
	return fmt.Sprintf("房间%d", roomID)
}
