package config

import "testing"

func TestMigrateGroupSubscriptionKeys(t *testing.T) {
	cfg := &Config{
		GroupSubscriptions: map[string][]int64{
			"1083074956":         {1279287},
			"qq:group:958843228": {9999},
		},
	}
	if !cfg.MigrateGroupSubscriptionKeys() {
		t.Fatal("expected migration to report a change")
	}
	if _, ok := cfg.GroupSubscriptions["1083074956"]; ok {
		t.Fatal("legacy bare key must be rewritten")
	}
	if _, ok := cfg.GroupSubscriptions["qq:group:1083074956"]; !ok {
		t.Fatal("expected canonical key")
	}
	if _, ok := cfg.GroupSubscriptions["qq:group:958843228"]; !ok {
		t.Fatal("already-canonical key must be preserved")
	}
	// idempotent
	if cfg.MigrateGroupSubscriptionKeys() {
		t.Fatal("second migration must be a no-op")
	}
}

func TestParseTargetID(t *testing.T) {
	if got := ParseTargetID("qq:group:1083074956"); got.Platform != "qq" || got.Kind != "group" || got.Address != "1083074956" {
		t.Fatalf("bad parse: %#v", got)
	}
	if got := ParseTargetID("feishu:private:ou_123"); got.Platform != "feishu" || got.Kind != "private" {
		t.Fatalf("bad parse: %#v", got)
	}
	if got := ParseTargetID("1083074956"); got.Platform != "qq" || got.Address != "1083074956" {
		t.Fatalf("legacy numeric should map to qq group: %#v", got)
	}
}

func TestResolveTargetFallsBackToParse(t *testing.T) {
	cfg := &Config{}
	got := cfg.ResolveTarget("feishu:group:oc_abc")
	if got.Platform != "feishu" || got.Address != "oc_abc" {
		t.Fatalf("resolve should parse unknown ids: %#v", got)
	}
}
