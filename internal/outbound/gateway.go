package outbound

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"pocket48-bot/internal/message"
)

// GatewayClient sends Pocket48 notifications through the standalone gateway.
// The existing OneBot client can therefore remain connected for inbound commands
// without being the business layer's outbound API.
type GatewayClient struct {
	endpoint string
	apiKey   string
	client   *http.Client
	queue    chan gatewayDelivery
}

type gatewayDelivery struct {
	Project      string            `json:"project"`
	Event        string            `json:"event"`
	LegacyTarget gatewayTarget     `json:"legacyTarget"`
	Segments     []message.Segment `json:"segments"`
}

type gatewayTarget struct {
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
}

func NewGatewayClient(baseURL, apiKey string) *GatewayClient {
	c := &GatewayClient{
		endpoint: strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/api/v1/messages",
		apiKey:   strings.TrimSpace(apiKey),
		client:   &http.Client{Timeout: 20 * time.Second},
		queue:    make(chan gatewayDelivery, 2000),
	}
	go c.run()
	return c
}

func (c *GatewayClient) Send(target Target, content interface{}) {
	if c == nil {
		return
	}
	address := strings.TrimSpace(target.Address)
	if address == "" && target.ID != 0 {
		address = fmt.Sprintf("%d", target.ID)
	}
	platform := strings.TrimSpace(target.Platform)
	if platform == "" {
		platform = "qq"
	}
	delivery := gatewayDelivery{
		Project: "pocket48",
		Event:   "notification",
		LegacyTarget: gatewayTarget{
			Platform: platform,
			Kind:     string(target.Kind),
			Address:  address,
		},
		Segments: gatewaySegments(content),
	}
	select {
	case c.queue <- delivery:
	default:
		log.Printf("[MessageGateway] status=queue_full target=%s", address)
	}
}

func gatewaySegments(content interface{}) []message.Segment {
	normalized := message.Normalize(content)
	switch value := normalized.(type) {
	case message.Segment:
		return []message.Segment{value}
	case []message.Segment:
		return value
	case string:
		return []message.Segment{message.Text(value)}
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return []message.Segment{message.Text(fmt.Sprint(value))}
		}
		var segments []message.Segment
		if err := json.Unmarshal(data, &segments); err == nil {
			return segments
		}
		return []message.Segment{message.Text(fmt.Sprint(value))}
	}
}

func (c *GatewayClient) QueueDepth() int {
	if c == nil {
		return 0
	}
	return len(c.queue)
}

func (c *GatewayClient) run() {
	for delivery := range c.queue {
		if err := c.deliver(delivery); err != nil {
			log.Printf("[MessageGateway] status=send_failed target=%s error=%v", delivery.LegacyTarget.Address, err)
		}
	}
}

func (c *GatewayClient) deliver(delivery gatewayDelivery) error {
	body, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gateway returned %s", resp.Status)
	}
	return nil
}
