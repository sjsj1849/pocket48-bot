package napcat

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"pocket48-bot/internal/config"

	"github.com/gorilla/websocket"
)

type Client struct {
	cfg      *config.Config
	conn     *websocket.Conn
	sendChan chan APIRequest
	mu       sync.Mutex

	OnGroupMessage   func(event *Event)
	OnPrivateMessage func(event *Event)
	OnMemberJoin     func(event *Event)

	isClosing bool
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		cfg:      cfg,
		sendChan: make(chan APIRequest, 2000), // Buffered channel
	}
}

func (c *Client) Connect() error {
	// Start the connection manager in a goroutine
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
				time.Sleep(5 * time.Second)
				continue
			}
		}
		// If connected, wait (conn should block until disconnect)
		time.Sleep(1 * time.Second)
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
	c.conn = conn
	log.Println("✅ Connected to NapCat successfully")

	// Start loops
	go c.readLoop()
	go c.writeLoop()

	return nil
}

func (c *Client) readLoop() {
	defer func() {
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
	}()

	for {
		if c.conn == nil {
			return
		}
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			// Check if clean close?
			log.Printf("⚠️ NapCat read error (disconnected?): %v", err)
			return // Exit readLoop -> manager will detect conn is nil (or we set it)
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
	// The actual WebSocket write is serialized by the mutex,
	// so more workers mainly help keep channel consumption responsive.
	const workerCount = 8
	var wg sync.WaitGroup

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
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
		}()
	}

	wg.Wait()
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
	log.Printf("[NAPCAT] Sending private message to %d: %+v", userID, message)
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
