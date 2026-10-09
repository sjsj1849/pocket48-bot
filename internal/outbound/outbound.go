// Package outbound owns the application-facing message delivery contract.
// Business code depends on Sender; platform clients are adapters behind it.
package outbound

import "pocket48-bot/internal/message"

type ChatKind string

const (
	GroupChat   ChatKind = "group"
	PrivateChat ChatKind = "private"
)

// Target identifies a destination without exposing a vendor API shape.
type Target struct {
	Platform string
	Kind     ChatKind
	ID       int64
	Address  string
}

// Sender is the single outbound boundary used by the application.
type Sender interface {
	Send(target Target, content interface{})
	QueueDepth() int
}

func Group(id int64) Target {
	return Target{Kind: GroupChat, ID: id}
}

func Private(id int64) Target {
	return Target{Kind: PrivateChat, ID: id}
}

func PlatformGroup(platform, address string) Target {
	return Target{Platform: platform, Kind: GroupChat, Address: address}
}

func SendGroup(sender Sender, id int64, content interface{}) {
	if sender != nil {
		sender.Send(Group(id), content)
	}
}

func SendPrivate(sender Sender, id int64, content interface{}) {
	if sender != nil {
		sender.Send(Private(id), content)
	}
}

// Re-export the neutral model from the outbound boundary for concise call sites.
type Segment = message.Segment

var (
	Text       = message.Text
	Image      = message.Image
	Mention    = message.Mention
	MentionAll = message.MentionAll
	Face       = message.Face
	Audio      = message.Audio
	Video      = message.Video
	Rich       = message.Rich
	Share      = message.Share
)

// SendToTargetIDs fans content out to explicit "platform:kind:address" target
// ids via a resolver. An empty target id list means the subscription has no
// destination configured, so nothing is delivered.
func SendToTargetIDs(sender Sender, resolve func(string) Target, targetIDs []string, content interface{}) {
	if sender == nil || resolve == nil {
		return
	}
	for _, targetID := range targetIDs {
		target := resolve(targetID)
		if target.Address == "" && target.ID == 0 {
			continue
		}
		sender.Send(target, content)
	}
}
