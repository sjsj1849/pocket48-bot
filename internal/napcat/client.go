package napcat

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"pocket48-bot/internal/config"

	"github.com/gorilla/websocket"
)

type Client struct {
	cfg        *config.Config
	conn       *websocket.Conn
	sendChan   chan APIRequest
	enqueueMu  sync.Mutex
	mu         sync.Mutex
	writerOnce sync.Once
	requestSeq atomic.Uint64
	pendingMu  sync.Mutex
	pending    map[string]chan apiResponse

	OnGroupMessage   func(event *Event)
	OnPrivateMessage func(event *Event)
	OnMemberJoin     func(event *Event)
	// OnConnectionChange is optional. Prefer silent reconnect: bot should NOT spam QQ here.
	// Panel/email observe [NapCat] status= lines in bot.log instead.
	OnConnectionChange func(connected bool, detail string)

	isClosing bool
	// wasConnected: true after the first successful session.
	wasConnected bool
	// loggedDown: true after we already logged status=disconnected for this outage.
	loggedDown bool
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:      cfg,
		sendChan: make(chan APIRequest, 2000), // Buffered channel
		pending:  make(map[string]chan apiResponse),
	}
}

type apiResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) Connect() error {
	c.writerOnce.Do(func() { go c.writeLoop() })
	go c.manager()
	return nil
}

func (c *Client) manager() {
	for {
		if c.isClosing {
			return
		}

		if c.conn == nil {
			err := c.connect()
			if err != nil {
				log.Printf("❌ Failed to connect to NapCat: %v. Retrying in 5s...", err)
				c.notifyDown(err.Error())
				time.Sleep(5 * time.Second)
				continue
			}
		}
		// If connected, wait (conn should block until disconnect)
		time.Sleep(1 * time.Second)
	}
}

func (c *Client) emitConnection(connected bool, detail string) {
	if c.OnConnectionChange == nil {
		return
	}
	go c.OnConnectionChange(connected, detail)
}

// notifyDown logs a single status=disconnected per outage for panel/email.
// Reconnect stays silent: no QQ pings, no repeated status spam every dial attempt.
func (c *Client) notifyDown(detail string) {
	c.mu.Lock()
	// Cold start before first ever connect: keep retrying quietly (manager already logs dial fails).
	if !c.wasConnected {
		c.mu.Unlock()
		return
	}
	if c.loggedDown {
		c.mu.Unlock()
		return
	}
	c.loggedDown = true
	c.mu.Unlock()
	log.Printf("[NapCat] status=disconnected message=%s", detail)
	c.emitConnection(false, detail)
}

func (c *Client) notifyUp() {
	c.mu.Lock()
	first := !c.wasConnected
	wasDown := c.loggedDown
	c.wasConnected = true
	c.loggedDown = false
	c.mu.Unlock()
	log.Printf("[NapCat] status=connected message=OneBot/llbot WebSocket 已连接")
	// Optional hook only on real recovery (not cold start). Callers must not spam QQ.
	if !first && wasDown {
		c.emitConnection(true, "connected")
	}
}

func (c *Client) connect() error {
	log.Print("[NapCat] 正在连接 OneBot WebSocket")
	headers := http.Header{}
	if c.cfg.NapCatAccessToken != "" {
		headers.Add("Authorization", "Bearer "+c.cfg.NapCatAccessToken)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(c.cfg.NapCatWSURL, headers)
	if err != nil {
		if resp != nil {
			log.Printf("Handshake failed with status: %s", resp.Status)
		}
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	log.Println("✅ Connected to NapCat successfully")
	c.notifyUp()

	// The writer is client-scoped so reconnects cannot create competing queue
	// consumers. A new reader is required for each websocket connection.
	go c.readLoop()

	return nil
}

func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.mu.Unlock()
		c.notifyDown("read loop ended")
	}()

	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Printf("⚠️ NapCat read error (disconnected?): %v", err)
			return
		}

		if c.handleAPIResponse(message) {
			continue
		}

		var event Event
		if err := json.Unmarshal(message, &event); err != nil {
			log.Printf("unmarshal error: %v", err)
			continue
		}

		c.handleEvent(&event)
	}
}

// handleAPIResponse completes the request identified by OneBot's echo field.
// NapCat may execute media sends asynchronously, so websocket write completion
// alone is not a delivery-order guarantee.
func (c *Client) handleAPIResponse(message []byte) bool {
	var envelope struct {
		Echo json.RawMessage `json:"echo"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil || len(envelope.Echo) == 0 || string(envelope.Echo) == "null" {
		return false
	}
	var echo string
	if err := json.Unmarshal(envelope.Echo, &echo); err != nil {
		echo = strings.Trim(string(envelope.Echo), `"`)
	}
	if echo == "" {
		return false
	}
	var response apiResponse
	if err := json.Unmarshal(message, &response); err != nil {
		return false
	}
	c.pendingMu.Lock()
	waiter := c.pending[echo]
	if waiter != nil {
		delete(c.pending, echo)
	}
	c.pendingMu.Unlock()
	if waiter == nil {
		return true
	}
	waiter <- response
	return true
}

func (c *Client) handleEvent(event *Event) {
	if event.PostType == "message" {
		if event.MessageType == "group" {
			if c.OnGroupMessage != nil {
				c.OnGroupMessage(event)
			}
		} else if event.MessageType == "private" {
			if c.OnPrivateMessage != nil {
				c.OnPrivateMessage(event)
			}
		}
	} else if event.PostType == "notice" {
		if event.NoticeType == "group_increase" {
			if c.OnMemberJoin != nil {
				c.OnMemberJoin(event)
			}
		}
	}
}

func (c *Client) writeLoop() {
	// Keep API requests FIFO through the actual OneBot response. A websocket
	// write only means NapCat accepted the command; remote media may still be
	// downloading and used to overtake/lag other messages in the same QQ chat.
	for req := range c.sendChan {
		if req.Echo == "" {
			req.Echo = fmt.Sprintf("bot48-%d", c.requestSeq.Add(1))
		}
		waiter := make(chan apiResponse, 1)
		c.pendingMu.Lock()
		c.pending[req.Echo] = waiter
		c.pendingMu.Unlock()
		started := time.Now()
		for {
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()

			if conn == nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}

			c.mu.Lock()
			if c.conn == nil {
				c.mu.Unlock()
				continue
			}
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := c.conn.WriteJSON(req)
			c.mu.Unlock()

			if err != nil {
				log.Printf("❌ NapCat write error: %v", err)
				c.mu.Lock()
				if c.conn != nil {
					c.conn.Close()
					c.conn = nil
				}
				c.mu.Unlock()
				time.Sleep(200 * time.Millisecond)
				continue
			}

			break
		}

		select {
		case response := <-waiter:
			elapsed := time.Since(started).Round(time.Millisecond)
			if response.Status != "ok" || response.RetCode != 0 {
				log.Printf("[NAPCAT] Delivery failed action=%s elapsed=%s retcode=%d message=%s", req.Action, elapsed, response.RetCode, response.Message)
			} else {
				log.Printf("[NAPCAT] Delivered action=%s elapsed=%s", req.Action, elapsed)
			}
		case <-time.After(15 * time.Minute):
			c.pendingMu.Lock()
			delete(c.pending, req.Echo)
			c.pendingMu.Unlock()
			log.Printf("[NAPCAT] Delivery response timeout action=%s elapsed=%s; releasing FIFO queue", req.Action, time.Since(started).Round(time.Second))
		}
	}
}

func (c *Client) SendGroupMessage(groupID int64, message interface{}) {
	// Keep at-all and the notification body in one QQ message. Move a leading
	// at-all below the title line so it remains on its own line, while the title
	// still occupies line one if QQ drops the at segment after quota exhaustion.
	message = normalizeLeadingAtAll(message)
	c.enqueueMu.Lock()
	defer c.enqueueMu.Unlock()
	c.enqueueGroupMessage(groupID, message)
}

func (c *Client) enqueueGroupMessage(groupID int64, message interface{}) {
	depth := len(c.sendChan)
	if depth > 50 {
		log.Printf("[NAPCAT-QUEUE] sendChan depth=%d (cap=%d) — queue building up", depth, cap(c.sendChan))
	}
	log.Printf("[NAPCAT] Sending group message to %d: %+v", groupID, message)
	c.sendChan <- APIRequest{
		Action: "send_group_msg",
		Params: SendGroupMsgParams{
			GroupID: groupID,
			Message: message,
		},
	}
}

// normalizeLeadingAtAll supports both segment slice shapes used by the bot.
// Only a leading at-all is moved; ordinary user mentions remain unchanged.
func normalizeLeadingAtAll(message interface{}) interface{} {
	switch segments := message.(type) {
	case []MessageSegment:
		if len(segments) == 0 || !isAtAll(segments[0]) {
			return message
		}
		remaining := trimLeadingLineBreaks(append([]MessageSegment(nil), segments[1:]...))
		if len(remaining) == 0 {
			return []MessageSegment{segments[0]}
		}
		return placeAtAllAfterTitle(segments[0], remaining)
	case []interface{}:
		if len(segments) == 0 {
			return message
		}
		first, valid := segments[0].(MessageSegment)
		if !valid || !isAtAll(first) {
			return message
		}
		remaining := trimLeadingInterfaceLineBreaks(append([]interface{}(nil), segments[1:]...))
		if len(remaining) == 0 {
			return []interface{}{first}
		}
		return placeInterfaceAtAllAfterTitle(first, remaining)
	default:
		return message
	}
}

func placeAtAllAfterTitle(atAll MessageSegment, body []MessageSegment) []MessageSegment {
	if body[0].Type != "text" {
		return append([]MessageSegment{atAll, TextSegment("\n")}, body...)
	}
	title, rest, found := strings.Cut(body[0].Data["text"], "\n")
	if !found || strings.TrimSpace(title) == "" {
		return append([]MessageSegment{atAll, TextSegment("\n")}, body...)
	}
	result := []MessageSegment{TextSegment(title + "\n"), atAll}
	if rest != "" {
		result = append(result, TextSegment("\n"+rest))
	}
	return append(result, body[1:]...)
}

func placeInterfaceAtAllAfterTitle(atAll MessageSegment, body []interface{}) []interface{} {
	first, valid := body[0].(MessageSegment)
	if !valid || first.Type != "text" {
		return append([]interface{}{atAll, TextSegment("\n")}, body...)
	}
	title, rest, found := strings.Cut(first.Data["text"], "\n")
	if !found || strings.TrimSpace(title) == "" {
		return append([]interface{}{atAll, TextSegment("\n")}, body...)
	}
	result := []interface{}{TextSegment(title + "\n"), atAll}
	if rest != "" {
		result = append(result, TextSegment("\n"+rest))
	}
	return append(result, body[1:]...)
}

func isAtAll(segment MessageSegment) bool {
	return segment.Type == "at" && segment.Data["qq"] == "all"
}

func trimLeadingLineBreaks(segments []MessageSegment) []MessageSegment {
	for len(segments) > 0 && segments[0].Type == "text" {
		text := strings.TrimLeft(segments[0].Data["text"], "\r\n")
		if text == "" {
			segments = segments[1:]
			continue
		}
		segments[0] = TextSegment(text)
		break
	}
	return segments
}

func trimLeadingInterfaceLineBreaks(segments []interface{}) []interface{} {
	for len(segments) > 0 {
		segment, valid := segments[0].(MessageSegment)
		if !valid || segment.Type != "text" {
			break
		}
		text := strings.TrimLeft(segment.Data["text"], "\r\n")
		if text == "" {
			segments = segments[1:]
			continue
		}
		segments[0] = TextSegment(text)
		break
	}
	return segments
}

func (c *Client) SendPrivateMessage(userID int64, message interface{}) {
	if segments, ok := message.([]MessageSegment); ok {
		hasBase64Image := false
		for _, segment := range segments {
			if segment.Type == "image" && strings.HasPrefix(segment.Data["file"], "base64://") {
				hasBase64Image = true
				break
			}
		}
		if hasBase64Image {
			log.Printf("[NAPCAT] Sending private message to %d: %d segments (base64 image redacted)", userID, len(segments))
		} else {
			log.Printf("[NAPCAT] Sending private message to %d: %+v", userID, message)
		}
	} else {
		log.Printf("[NAPCAT] Sending private message to %d: %+v", userID, message)
	}
	c.sendChan <- APIRequest{
		Action: "send_private_msg",
		Params: SendPrivateMsgParams{
			UserID:  userID,
			Message: message,
		},
	}
}

// QueueDepth returns the current number of pending messages in the send channel.
func (c *Client) QueueDepth() int {
	return len(c.sendChan)
}
