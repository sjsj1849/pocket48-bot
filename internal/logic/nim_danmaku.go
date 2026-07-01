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
	"syscall"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"

	"github.com/gorilla/websocket"
)

// NimDanmakuBridge manages the Node.js sidecar for NIM chatroom danmaku.
type NimDanmakuBridge struct {
	cfg         *config.Config
	cmd         *exec.Cmd
	conn        *websocket.Conn
	mu          sync.Mutex
	connected   bool
	currentRoom int64
	stopCh      chan struct{}
	wg          sync.WaitGroup
	onDanmaku   func(roomID int64, d *DanmakuMessage)
	onGift      func(roomID int64, g *GiftMessage)
	onMemberEvent func(roomID int64, m *MemberEvent)
	onConnected func(roomID int64)
	onError     func(err error)
}

// DanmakuMessage represents a live danmaku (弹幕) message.
type DanmakuMessage struct {
	Type   string `json:"type"`   // "text", "barrage", "member_barrage"
	Nick   string `json:"nick"`   // sender nickname
	From   string `json:"from"`   // sender ID
	Text   string `json:"text"`   // message text
	Avatar string `json:"avatar,omitempty"`
	Time   int64  `json:"time"`   // timestamp
}

// GiftMessage represents a live gift notification.
type GiftMessage struct {
	Nick     string `json:"nick"`
	From     string `json:"from"`
	GiftName string `json:"giftName"`
	GiftNum  int    `json:"giftNum"`
	GiftID   int    `json:"giftId"`
	Receiver string `json:"receiver,omitempty"`
	Avatar   string `json:"avatar,omitempty"`
	Time     int64  `json:"time"`
}

// MemberEvent represents a member enter/leave notification from the chatroom.
type MemberEvent struct {
	Event  string `json:"event"`  // "memberEnter" or "memberExit"
	UserID string `json:"userId"`
	Nick   string `json:"nick"`
	Time   int64  `json:"time"`
}

// sidecarEvent is the JSON message from the Node.js sidecar.
type sidecarEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
	Msg  string          `json:"msg,omitempty"`
	Port int             `json:"port,omitempty"`
	RoomID int64         `json:"roomId,omitempty"`
	Code  int            `json:"code,omitempty"`
}

// sidecarCommand is a JSON command sent to the sidecar.
type sidecarCommand struct {
	Cmd    string `json:"cmd"`
	RoomID int64  `json:"roomId,omitempty"`
	LiveID string `json:"liveId,omitempty"`
}

// NewNimDanmakuBridge creates a new danmaku bridge manager.
func NewNimDanmakuBridge(cfg *config.Config) *NimDanmakuBridge {
	return &NimDanmakuBridge{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// SetCallbacks registers event handlers.
func (b *NimDanmakuBridge) SetCallbacks(
	onDanmaku func(roomID int64, d *DanmakuMessage),
	onGift func(roomID int64, g *GiftMessage),
	onMemberEvent func(roomID int64, m *MemberEvent),
	onConnected func(roomID int64),
	onError func(err error),
) {
	b.onDanmaku = onDanmaku
	b.onGift = onGift
	b.onMemberEvent = onMemberEvent
	b.onConnected = onConnected
	b.onError = onError
}

// Start launches the Node.js sidecar and connects to its WebSocket.
func (b *NimDanmakuBridge) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn != nil {
		return fmt.Errorf("bridge already started")
	}

	// Start the Node.js sidecar process
	sidecarPath := b.cfg.NIMSidecarCmd
	if sidecarPath == "" {
		sidecarPath = "./sidecar/nim-bridge/index.mjs"
	}

	cmd := exec.Command("node", sidecarPath, "--wsPort=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sidecar: %w", err)
	}
	b.cmd = cmd

	// Read stderr in background for logging
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			// Try to parse as JSON log
			var evt sidecarEvent
			if json.Unmarshal([]byte(line), &evt) == nil && evt.Type == "log" {
				log.Printf("[NIM-bridge] %s", evt.Msg)
			} else {
				log.Printf("[NIM-bridge] %s", line)
			}
		}
	}()

	// Read the port from stdout
	portCh := make(chan int, 1)
	errCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "PORT:") {
				portStr := strings.TrimPrefix(line, "PORT:")
				port, err := strconv.Atoi(portStr)
				if err != nil {
					errCh <- fmt.Errorf("parse port %q: %w", portStr, err)
					return
				}
				portCh <- port
				return
			}
			log.Printf("[NIM-bridge:stdout] %s", line)
		}
		err := scanner.Err()
		if err != nil {
			errCh <- err
		} else {
			errCh <- fmt.Errorf("sidecar exited before reporting port")
		}
	}()

	// Wait for port with timeout
	var port int
	select {
	case port = <-portCh:
	case err := <-errCh:
		b.cmd.Process.Kill()
		return fmt.Errorf("sidecar startup: %w", err)
	case <-time.After(10 * time.Second):
		b.cmd.Process.Kill()
		return fmt.Errorf("sidecar startup timeout (10s)")
	}

	// Connect WebSocket to sidecar
	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		b.cmd.Process.Kill()
		return fmt.Errorf("connect to sidecar ws: %w", err)
	}
	b.conn = conn

	// Start reader goroutine
	b.wg.Add(1)
	go b.readLoop()

	log.Printf("[NIM-danmaku] Bridge started, sidecar PID=%d, ws=127.0.0.1:%d", cmd.Process.Pid, port)
	return nil
}

// Stop terminates the sidecar and cleans up.
func (b *NimDanmakuBridge) Stop() {
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	b.mu.Unlock()

	if conn != nil {
		conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
		conn.Close()
	}

	if b.cmd != nil && b.cmd.Process != nil {
		b.cmd.Process.Signal(gracefulShutdownSignal)
		go func() {
			time.Sleep(3 * time.Second)
			b.cmd.Process.Kill()
		}()
		b.cmd.Wait()
	}

	close(b.stopCh)
	b.wg.Wait()
	log.Printf("[NIM-danmaku] Bridge stopped")
}

// ConnectRoom tells the sidecar to join a NIM chatroom for danmaku.
func (b *NimDanmakuBridge) ConnectRoom(roomID int64, liveID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn == nil {
		return fmt.Errorf("bridge not started")
	}

	cmd := sidecarCommand{
		Cmd:    "connect",
		RoomID: roomID,
		LiveID: liveID,
	}
	b.currentRoom = roomID

	data, _ := json.Marshal(cmd)
	return b.conn.WriteMessage(websocket.TextMessage, data)
}

// DisconnectRoom tells the sidecar to leave the current chatroom.
func (b *NimDanmakuBridge) DisconnectRoom() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.conn == nil {
		return nil
	}

	b.currentRoom = 0
	cmd := sidecarCommand{Cmd: "disconnect"}
	data, _ := json.Marshal(cmd)
	return b.conn.WriteMessage(websocket.TextMessage, data)
}

// IsConnected returns whether the bridge is connected to a chatroom.
func (b *NimDanmakuBridge) IsConnected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connected
}

// CurrentRoomID returns the currently connected room ID.
func (b *NimDanmakuBridge) CurrentRoomID() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentRoom
}

func (b *NimDanmakuBridge) readLoop() {
	defer b.wg.Done()

	for {
		select {
		case <-b.stopCh:
			return
		default:
		}

		b.mu.Lock()
		conn := b.conn
		b.mu.Unlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[NIM-danmaku] read error: %v", err)
			}
			b.mu.Lock()
			b.connected = false
			b.mu.Unlock()
			return
		}

		var evt sidecarEvent
		if err := json.Unmarshal(message, &evt); err != nil {
			log.Printf("[NIM-danmaku] parse event error: %v (raw: %s)", err, string(message))
			continue
		}

		switch evt.Type {
		case "connected":
			b.mu.Lock()
			b.connected = true
			roomID := b.currentRoom
			b.mu.Unlock()
			if b.onConnected != nil {
				b.onConnected(roomID)
			}
			log.Printf("[NIM-danmaku] Connected to chatroom %d", roomID)

		case "disconnected":
			b.mu.Lock()
			b.connected = false
			b.mu.Unlock()
			log.Printf("[NIM-danmaku] Disconnected: %s", evt.Msg)

		case "danmaku":
			var d DanmakuMessage
			if err := json.Unmarshal(evt.Data, &d); err != nil {
				log.Printf("[NIM-danmaku] parse danmaku error: %v", err)
				continue
			}
			b.mu.Lock()
			roomID := b.currentRoom
			b.mu.Unlock()
			if b.onDanmaku != nil {
				b.onDanmaku(roomID, &d)
			}

		case "gift":
			var g GiftMessage
			if err := json.Unmarshal(evt.Data, &g); err != nil {
				log.Printf("[NIM-danmaku] parse gift error: %v", err)
				continue
			}
			b.mu.Lock()
			roomID := b.currentRoom
			b.mu.Unlock()
			if b.onGift != nil {
				b.onGift(roomID, &g)
			}

		case "member_event":
			var m MemberEvent
			if err := json.Unmarshal(evt.Data, &m); err != nil {
				log.Printf("[NIM-danmaku] parse member_event error: %v", err)
				continue
			}
			b.mu.Lock()
			roomID := b.currentRoom
			b.mu.Unlock()
			if b.onMemberEvent != nil {
				b.onMemberEvent(roomID, &m)
			}

		case "error":
			log.Printf("[NIM-danmaku] Error: %s (code=%d)", evt.Msg, evt.Code)
			if b.onError != nil {
				b.onError(fmt.Errorf("sidecar error: %s (code=%d)", evt.Msg, evt.Code))
			}

		case "log":
			log.Printf("[NIM-danmaku] %s", evt.Msg)

		case "status":
			// Status response, ignore
		}
	}
}

// gracefulShutdownSignal is SIGTERM on Unix.
var gracefulShutdownSignal = syscall.SIGTERM

// connectDanmakuForLive fetches the NIM chatroom ID for a live stream and tells the bridge to connect.
// roomIDFromPush is the NIM roomId from the push body (may be 0 if not included).
// liveID is the Pocket48 live stream ID (for fallback lookup).
func (b *Bot) connectDanmakuForLive(liveID string, roomIDFromPush int64, room *pocket48.RoomInfo) {
	// Priority: roomId from push body > GetLiveOne API
	roomID := roomIDFromPush
	if roomID == 0 && liveID != "" {
		liveOne, err := b.pocket.GetLiveOne(liveID)
		if err != nil {
			log.Printf("[NIM-danmaku] GetLiveOne failed for live %s: %v", liveID, err)
			return
		}
		roomID = liveOne.RoomID
	}

	if roomID == 0 {
		log.Printf("[NIM-danmaku] No NIM roomId available for live (liveID=%s)", liveID)
		return
	}

	log.Printf("[NIM-danmaku] Connecting to live chatroom roomId=%d for %s (liveId=%s)",
		roomID, room.OwnerName, liveID)

	if err := b.nimDanmaku.ConnectRoom(roomID, liveID); err != nil {
		log.Printf("[NIM-danmaku] ConnectRoom failed: %v", err)
	}
}

// --- Bot callback handlers for danmaku events ---

// handleDanmakuMessage is called when a danmaku message is received from the sidecar.
// Only forwards messages from OTHER idols visiting the live room (not the room owner, not fans).
func (b *Bot) handleDanmakuMessage(roomID int64, d *DanmakuMessage) {
	if d.Text == "" {
		return
	}

	// Skip fans (non-stars)
	fromID, _ := strconv.ParseInt(d.From, 10, 64)
	if fromID == 0 || !b.isKnownStar(fromID) {
		return
	}

	// Skip the room owner (the streamer themself)
	ownerID := b.getRoomOwnerID(roomID)
	if fromID == ownerID {
		return
	}

	// Find target groups for this room
	targetGroups := b.getTargetGroupsForRoom(roomID)
	if len(targetGroups) == 0 {
		return
	}

	// Get room name
	roomName := b.getRoomNameForDanmaku(roomID)

	segments := []interface{}{
		napcat.TextSegment(fmt.Sprintf("💬 %s直播间 · %s: %s", roomName, d.Nick, d.Text)),
	}
	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, segments)
	}
}

// handleDanmakuGift is called when a gift notification is received from the sidecar.
// Only forwards gifts from other idols (not room owner, not fans).
func (b *Bot) handleDanmakuGift(roomID int64, g *GiftMessage) {
	fromID, _ := strconv.ParseInt(g.From, 10, 64)
	if fromID == 0 || !b.isKnownStar(fromID) {
		return
	}

	ownerID := b.getRoomOwnerID(roomID)
	if fromID == ownerID {
		return
	}

	targetGroups := b.getTargetGroupsForRoom(roomID)
	if len(targetGroups) == 0 {
		return
	}

	roomName := b.getRoomNameForDanmaku(roomID)

	text := fmt.Sprintf("🎁 %s直播间 · %s 送了 %d 个%s",
		roomName, g.Nick, g.GiftNum, g.GiftName)
	if g.Receiver != "" {
		text += fmt.Sprintf(" 给 %s", g.Receiver)
	}

	segments := []interface{}{
		napcat.TextSegment(text),
	}
	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, segments)
	}
}

// getRoomOwnerID returns the owner userId of a room from the room info cache.
func (b *Bot) getRoomOwnerID(roomID int64) int64 {
	info, err := b.getCachedRoomInfo(roomID)
	if err == nil && info != nil {
		return info.OwnerID
	}
	return 0
}

// handleMemberEvent is called when a member enters or leaves the live chatroom.
// Only reports events for other idols (not room owner, not fans).
func (b *Bot) handleMemberEvent(roomID int64, m *MemberEvent) {
	fromID, _ := strconv.ParseInt(m.UserID, 10, 64)
	if fromID == 0 || !b.isKnownStar(fromID) {
		return
	}

	ownerID := b.getRoomOwnerID(roomID)
	if fromID == ownerID {
		return
	}

	targetGroups := b.getTargetGroupsForRoom(roomID)
	if len(targetGroups) == 0 {
		return
	}

	roomName := b.getRoomNameForDanmaku(roomID)
	now := time.Now()
	timeStr := now.Format("2006-01-02 15:04:05")

	var msg string

	if m.Event == "memberEnter" {
		// Track enter time
		b.memberEnterMu.Lock()
		b.memberEnterTimes[m.UserID] = now
		b.memberEnterMu.Unlock()

		msg = fmt.Sprintf("👀 %s进入了%s的直播间\n%s", m.Nick, roomName, timeStr)
	} else if m.Event == "memberExit" {
		// Calculate duration from enter time
		durationStr := ""
		b.memberEnterMu.Lock()
		if enterTime, ok := b.memberEnterTimes[m.UserID]; ok {
			watched := now.Sub(enterTime)
			delete(b.memberEnterTimes, m.UserID)
			if watched >= time.Minute {
				mins := int(watched.Minutes())
				secs := int(watched.Seconds()) % 60
				if mins > 0 {
					durationStr = fmt.Sprintf("观看时长%d分钟", mins)
					if secs > 0 && mins < 10 {
						durationStr = fmt.Sprintf("观看时长%d分%d秒", mins, secs)
					}
				} else {
					durationStr = fmt.Sprintf("观看时长%d秒", secs)
				}
			}
		}
		b.memberEnterMu.Unlock()

		msg = fmt.Sprintf("👀 %s离开了%s的直播间\n%s\n%s", m.Nick, roomName, durationStr, timeStr)
	} else {
		return
	}

	segments := []interface{}{napcat.TextSegment(msg)}
	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, segments)
	}
}

// isKnownStar checks if a user is a known SNH48 star/idol.
// Uses a cached set of star userIds populated from:
//   - Room owners of monitored rooms
//   - Previously looked-up star statuses
func (b *Bot) isKnownStar(userID int64) bool {
	if userID == 0 {
		return false
	}

	b.mu.RLock()
	cached, ok := b.userDetailCache[userID]
	b.mu.RUnlock()
	if ok {
		return cached.info != nil && cached.info.IsStar
	}

	// Not in cache, try to look up via API
	detail, err := b.pocket.GetUserDetailInfo(userID)
	if err != nil {
		// API error, log and assume not a star
		log.Printf("[NIM-danmaku] Failed to lookup user %d: %v", userID, err)
		return false
	}

	// Cache the result
	b.mu.Lock()
	b.userDetailCache[userID] = cachedUserDetail{info: detail, expiresAt: time.Now().Add(10 * time.Minute)}
	b.mu.Unlock()

	return detail.IsStar
}

// handleDanmakuConnected is called when the sidecar connects to a chatroom.
func (b *Bot) handleDanmakuConnected(roomID int64) {
	log.Printf("[NIM-danmaku] Connected to live chatroom roomId=%d", roomID)
}

// handleDanmakuError is called when the sidecar reports an error.
func (b *Bot) handleDanmakuError(err error) {
	log.Printf("[NIM-danmaku] Error: %v", err)
}

// getRoomNameForDanmaku returns the room name for display in danmaku messages.
func (b *Bot) getRoomNameForDanmaku(roomID int64) string {
	// Try to get from room info cache
	info, err := b.getCachedRoomInfo(roomID)
	if err == nil && info != nil && info.ChannelName != "" {
		return info.ChannelName
	}
	return fmt.Sprintf("房间%d", roomID)
}

