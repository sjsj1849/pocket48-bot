package config

import (
	"strconv"
	"strings"
)

// DeliveryTarget is a platform-aware destination. Legacy subscriptions are
// keyed by QQ group id, so this address book is the bridge that lets a
// subscription point at QQ or Feishu, a group or a private chat.
type DeliveryTarget struct {
	ID       string `json:"id"`
	Platform string `json:"platform"` // qq | feishu
	Kind     string `json:"kind"`     // group | private
	Address  string `json:"address"`
	Name     string `json:"name"`
}

func (t DeliveryTarget) Key() string { return t.Platform + ":" + t.Kind + ":" + t.Address }

// SeedDeliveryTargets builds the first address book from what is already
// configured, so nothing breaks on upgrade: every existing QQ group and the
// super administrator become targets, and Feishu mirrors are added too.
func (c *Config) SeedDeliveryTargets() {
	if c.DeliveryTargets == nil {
		c.DeliveryTargets = []DeliveryTarget{}
	}
	seen := make(map[string]bool, len(c.DeliveryTargets))
	for _, target := range c.DeliveryTargets {
		seen[target.Key()] = true
	}
	add := func(platform, kind, address, name string) {
		address = strings.TrimSpace(address)
		if address == "" {
			return
		}
		key := platform + ":" + kind + ":" + address
		if seen[key] {
			return
		}
		seen[key] = true
		c.DeliveryTargets = append(c.DeliveryTargets, DeliveryTarget{
			ID: key, Platform: platform, Kind: kind, Address: address, Name: name,
		})
	}
	for groupID := range c.GroupSubscriptions {
		add("qq", "group", groupID, "QQ 群 "+groupID)
	}
	for groupID := range c.WeiboSubscriptions {
		id := strconv.FormatInt(groupID, 10)
		add("qq", "group", id, "QQ 群 "+id)
	}
	for groupID := range c.DouyinSubscriptions {
		id := strconv.FormatInt(groupID, 10)
		add("qq", "group", id, "QQ 群 "+id)
	}
	for groupID := range c.XiaohongshuSubscriptions {
		id := strconv.FormatInt(groupID, 10)
		add("qq", "group", id, "QQ 群 "+id)
	}
	if c.SuperAdmin != 0 {
		add("qq", "private", strconv.FormatInt(c.SuperAdmin, 10), "超管私聊")
	}
	for _, destination := range c.FeishuRoutes() {
		add("feishu", "group", destination, "飞书群 "+destination)
	}
	for _, destination := range c.FeishuPrivateRouteMap() {
		add("feishu", "private", destination, "飞书私聊 "+destination)
	}
}

// TargetByID resolves an address book entry. An empty id means "use the legacy
// group id", which keeps current behaviour intact during migration.
func (c *Config) TargetByID(id string) (DeliveryTarget, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return DeliveryTarget{}, false
	}
	for _, target := range c.DeliveryTargets {
		if target.ID == id {
			return target, true
		}
	}
	return DeliveryTarget{}, false
}

// ParseTargetID understands "platform:kind:address" and, for backward
// compatibility, a bare numeric string which means a QQ group.
func ParseTargetID(id string) DeliveryTarget {
	id = strings.TrimSpace(id)
	if id == "" {
		return DeliveryTarget{}
	}
	parts := strings.SplitN(id, ":", 3)
	if len(parts) == 3 {
		return DeliveryTarget{ID: id, Platform: parts[0], Kind: parts[1], Address: parts[2]}
	}
	if len(parts) == 1 {
		// Legacy: a bare number is a QQ group id.
		if _, err := strconv.ParseInt(id, 10, 64); err == nil {
			return DeliveryTarget{ID: "qq:group:" + id, Platform: "qq", Kind: "group", Address: id}
		}
	}
	return DeliveryTarget{}
}

// TargetIDForQQGroup returns the canonical target id for a legacy QQ group key.
func TargetIDForQQGroup(groupID string) string {
	return "qq:group:" + strings.TrimSpace(groupID)
}

// TargetIDForQQPrivate returns the canonical target id for a QQ private chat.
func TargetIDForQQPrivate(userID string) string {
	return "qq:private:" + strings.TrimSpace(userID)
}

// ResolveTarget converts a target id (or legacy group key) into the delivery
// target used by the outbound hub. It consults the address book so names and
// aliases are honoured, but falls back to parsing the id itself.
func (c *Config) ResolveTarget(id string) DeliveryTarget {
	if target, ok := c.TargetByID(id); ok {
		return target
	}
	if parsed := ParseTargetID(id); parsed.ID != "" {
		return parsed
	}
	return DeliveryTarget{}
}

// MigrateGroupSubscriptionKeys rewrites GROUP_SUBSCRIPTIONS keys from bare QQ
// group ids to canonical "qq:group:<id>" target ids in place. Idempotent.
func (c *Config) MigrateGroupSubscriptionKeys() bool {
	if c.GroupSubscriptions == nil {
		return false
	}
	changed := false
	rewritten := make(map[string][]int64, len(c.GroupSubscriptions))
	for key, roomIDs := range c.GroupSubscriptions {
		if strings.Contains(key, ":") {
			rewritten[key] = roomIDs
			continue
		}
		rewritten[TargetIDForQQGroup(key)] = roomIDs
		changed = true
	}
	if changed {
		c.GroupSubscriptions = rewritten
	}
	return changed
}
