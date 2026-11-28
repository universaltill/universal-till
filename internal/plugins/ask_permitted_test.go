package plugins

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEventBus_AskPermitted_OnlyAHolderAnswers: the ask goes only to a
// subscriber whose manifest declared AND whose operator granted the extra
// permission (ut-docs#7108, payout:suggest); a subscriber without it never
// sees the payload, even when it subscribed first.
func TestEventBus_AskPermitted_OnlyAHolderAnswers(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()
	const ev = "tips.suggest.ask"

	for _, c := range []struct {
		id    string
		perms []string
		grant []string
	}{
		{"plugin-nogrant", []string{"events:receive", "payout:suggest"}, []string{"events:receive"}},
		{"plugin-undeclared", []string{"events:receive"}, []string{"events:receive"}},
		{"plugin-holder", []string{"events:receive", "payout:suggest"}, []string{"events:receive", "payout:suggest"}},
	} {
		m := &Manifest{ID: c.id, Name: c.id, Version: "1.0.0", Entrypoint: "./test",
			Hooks: []ManifestHook{{Event: ev, Action: "tips"}}, Permissions: c.perms}
		if err := PersistManifest(ctx, db, m, InstallOptions{}); err != nil {
			t.Fatalf("persist %s: %v", c.id, err)
		}
		for _, p := range c.grant {
			if err := GrantPermission(ctx, db, c.id, p); err != nil {
				t.Fatalf("grant %s %s: %v", c.id, p, err)
			}
		}
	}
	bus := NewEventBus(db)
	bus.SetEventMode(ev, Blocking)
	called := map[string]bool{}
	for _, id := range []string{"plugin-nogrant", "plugin-undeclared", "plugin-holder"} {
		id := id
		if _, err := bus.SubscribeWithHandler(ctx, id, []string{ev}, func(context.Context, Event) (json.RawMessage, error) {
			called[id] = true
			return json.RawMessage(`{"from":"` + id + `"}`), nil
		}); err != nil {
			t.Fatalf("subscribe %s: %v", id, err)
		}
	}
	resp, ok, err := bus.AskPermitted(ctx, ev, "payout:suggest", map[string]any{"from": "2026-11-01"})
	if err != nil || !ok || string(resp) != `{"from":"plugin-holder"}` {
		t.Fatalf("resp=%s ok=%v err=%v, want the holder's answer", resp, ok, err)
	}
	if called["plugin-nogrant"] || called["plugin-undeclared"] {
		t.Fatalf("a plugin without the permission was called: %v", called)
	}
}
