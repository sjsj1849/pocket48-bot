package napcat

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/config"

	"github.com/gorilla/websocket"
)

type Client struct {
	cfg        *config.Config
	conn       *websocket.Conn
	sendChan   chan APIRequest
	mu         sync.Mutex
	writerOnce sync.Once

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
	}
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
	log.Printf("Connecting to %s with Token: '%s'", c.cfg.NapCatWSURL, c.cfg.NapCatAccessToken)
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

		var event Event
		if err := json.Unmarshal(message, &event); err != nil {
			log.Printf("unmarshal error: %v", err)
			continue
		}

		c.handleEvent(&event)
	}
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
	// Keep API requests FIFO. Multiple workers still serialize on the websocket
	// mutex but may acquire it out of order, which is visible for media bursts.
	for req := range c.sendChan {
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
	}
}

func (c *Client) SendGroupMessage(groupID int64, message interface{}) {
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
