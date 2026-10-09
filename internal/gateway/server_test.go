package gateway

import (
	"path/filepath"
	"testing"
)

func TestResolveTargetsSupportsIndependentAndLegacyRoutes(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	cfg.QQ.Enabled = false
	cfg.Targets = []Target{
		{ID: "qq-main", Name: "QQ 主群", Platform: "qq", Kind: "group", Address: "123"},
		{ID: "feishu-main", Name: "飞书主群", Platform: "feishu", Kind: "group", Address: "oc_123"},
	}
	cfg.Routes = []Route{{
		ID: "legacy", Name: "迁移路由", Project: "pocket48", Event: "*",
		LegacyPlatform: "qq", LegacyAddress: "123", TargetIDs: []string{"qq-main", "feishu-main"}, Enabled: true,
	}}
	if err := store.Update(cfg); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	targets := server.resolveTargets(MessageRequest{
		Project: "pocket48", Event: "notification",
		LegacyTarget: &LegacyTarget{Platform: "qq", Kind: "group", Address: "123"},
	})
	if len(targets) != 2 || targets[0].ID != "qq-main" || targets[1].ID != "feishu-main" {
		t.Fatalf("unexpected targets: %#v", targets)
	}

	direct := server.resolveTargets(MessageRequest{TargetIDs: []string{"feishu-main"}})
	if len(direct) != 1 || direct[0].ID != "feishu-main" {
		t.Fatalf("unexpected direct targets: %#v", direct)
	}
}

func TestResolveTargetsFallsBackToLegacyTarget(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	cfg.QQ.Enabled = false
	if err := store.Update(cfg); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store)
	if err != nil {
		t.Fatal(err)
	}
	// Empty targets/routes is the state right after a fresh deployment. Messages
	// must still reach the legacy QQ destination instead of being dropped.
	targets := server.resolveTargets(MessageRequest{
		Project: "pocket48", Event: "notification",
		LegacyTarget: &LegacyTarget{Platform: "qq", Kind: "group", Address: "123"},
	})
	if len(targets) != 1 {
		t.Fatalf("expected legacy fallback target, got %#v", targets)
	}
	if targets[0].Platform != "qq" || targets[0].Kind != "group" || targets[0].Address != "123" {
		t.Fatalf("unexpected fallback target: %#v", targets[0])
	}
	if _, err := outboundTarget(targets[0]); err != nil {
		t.Fatalf("fallback target must convert: %v", err)
	}

	// Without a legacy address there is nothing to fall back to.
	if got := server.resolveTargets(MessageRequest{Project: "pocket48", Event: "notification"}); len(got) != 0 {
		t.Fatalf("expected no targets, got %#v", got)
	}
}

func TestStorePersistsGatewayAddressBook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	cfg.Targets = []Target{{ID: "private", Name: "飞书私聊", Platform: "feishu", Kind: "private", Address: "ou_123", Favorite: true}}
	if err := store.Update(cfg); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get().Targets; len(got) != 1 || got[0].Kind != "private" || !got[0].Favorite {
		t.Fatalf("unexpected targets after reopen: %#v", got)
	}
}
