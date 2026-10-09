package outbound

import (
	"log"
	"sync"

	"pocket48-bot/internal/message"
)

// Hub routes messages to registered platform adapters. An empty platform uses
// the configured default, allowing existing subscriptions to keep numeric IDs
// while new routes can explicitly select feishu, telegram, and so on.
type Hub struct {
	mu              sync.RWMutex
	defaultPlatform string
	adapters        map[string]Sender
}

func NewHub(defaultPlatform string) *Hub {
	return &Hub{defaultPlatform: defaultPlatform, adapters: make(map[string]Sender)}
}

// SetDefaultPlatform picks where messages without an explicit platform go. It
// lets an operator turn QQ off and still keep Feishu as the working outlet.
func (h *Hub) SetDefaultPlatform(platform string) {
	if h == nil || platform == "" {
		return
	}
	h.mu.Lock()
	h.defaultPlatform = platform
	h.mu.Unlock()
}

func (h *Hub) Register(platform string, sender Sender) {
	if h == nil || platform == "" || sender == nil {
		return
	}
	h.mu.Lock()
	h.adapters[platform] = sender
	h.mu.Unlock()
}

func (h *Hub) Send(target Target, content interface{}) {
	if h == nil {
		return
	}
	normalized := message.Normalize(content)
	h.mu.RLock()
	platform := target.Platform
	if platform == "" {
		platform = h.defaultPlatform
	}
	adapter := h.adapters[platform]
	h.mu.RUnlock()
	if adapter == nil {
		log.Printf("[Outbound] no adapter registered for platform=%q", platform)
		return
	}
	target.Platform = platform
	adapter.Send(target, normalized)
}

func (h *Hub) QueueDepth() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := 0
	for _, adapter := range h.adapters {
		total += adapter.QueueDepth()
	}
	return total
}
