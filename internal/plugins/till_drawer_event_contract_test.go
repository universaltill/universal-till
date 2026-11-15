package plugins

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"
)

// ut-docs#5758: the till.drawer.no_sale contract
// (ut-docs reference/contracts/till-drawer-no-sale.md).

func TestTillDrawerNoSale_Registration(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	const name = "till.drawer.no_sale"
	if TillDrawerNoSaleEvent != name {
		t.Fatalf("TillDrawerNoSaleEvent = %q, want %q", TillDrawerNoSaleEvent, name)
	}
	if mode := NewEventBus(db).GetEventMode(TillDrawerNoSaleEvent); mode != NonBlocking {
		t.Fatalf("till.drawer.no_sale must be NonBlocking, got %v", mode)
	}
	if bad := regexp.MustCompile(`fiscal|tse|\.ask$|\.authorize$|\.refund$`); bad.MatchString(name) {
		t.Fatalf("event name %q would be marked Blocking or is not neutral", name)
	}
	classes, ok := EventDataClasses(name)
	if !ok {
		t.Fatal("till.drawer.no_sale has no data-class entry (fails closed for ai.agent)")
	}
	got := map[string]bool{}
	for _, c := range classes {
		got[string(c)] = true
	}
	if len(got) != 2 || !got["sales"] || !got["staff"] {
		t.Fatalf("data classes = %v, want {sales, staff}", classes)
	}
}

func TestTillDrawerNoSalePayload_WireShape(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	manifest := &Manifest{
		ID: "com.example.signer", Name: "Signer", Version: "1.0.0", Entrypoint: "./s",
		Hooks:       []ManifestHook{{Event: TillDrawerNoSaleEvent, Action: "signer.record"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bus := NewEventBus(db)
	ch, err := bus.Subscribe(ctx, manifest.ID, []string{TillDrawerNoSaleEvent})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := bus.PublishTillDrawerNoSale(ctx, TillDrawerNoSalePayload{
		NoSaleID: "ns-1", RegisterID: "reg-1", ActorID: "u1", CreatedAt: now,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case ev := <-ch:
		if ev.Type != TillDrawerNoSaleEvent {
			t.Fatalf("type = %q", ev.Type)
		}
		var raw map[string]any
		if err := json.Unmarshal(ev.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		want := []string{"no_sale_id", "register_id", "till_id", "actor_id", "approver_id", "created_at"}
		if len(raw) != len(want) {
			t.Fatalf("payload keys = %v, want exactly %v", raw, want)
		}
		for _, k := range want {
			v, present := raw[k]
			if _, isStr := v.(string); !present || !isStr {
				t.Fatalf("key %q must be present and a string, got %v (%s)", k, v, ev.Payload)
			}
		}
		if raw["approver_id"] != "" || raw["till_id"] != "" || raw["created_at"] != now {
			t.Fatalf("unexpected values: %s", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber never received till.drawer.no_sale")
	}
}

// ut-docs#5758: `till` is a core event root, so no plugin can forge
// till.drawer.no_sale (own-prefix rule) or install under that id.
func TestPluginID_TillRootRefused(t *testing.T) {
	for _, id := range []string{"till", "till.drawer"} {
		if err := validatePluginID(id); err == nil {
			t.Errorf("validatePluginID(%q) = nil, want an error", id)
		}
	}
	if err := checkPluginEventName("com.acme", "till.drawer.no_sale"); err != errEventNameCoreRoot {
		t.Fatalf("checkPluginEventName = %v, want errEventNameCoreRoot", err)
	}
}
