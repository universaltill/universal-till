package plugins

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The stock.adjusted payload is the stable contract inventory/ERP connector
// plugins depend on to keep external stock levels in sync (ADR-0014). This test
// pins the wire shape: a subscriber must receive the item/variant/SKU, the
// signed decimal delta (weighed goods), the reason, and the location — so a
// change that drops or renames a field is caught before it breaks connectors.
func TestStockAdjustedEvent_ConnectorContract(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	// A realistic connector: an integration plugin hooking stock.adjusted.
	manifest := &Manifest{
		ID:          "com.example.inventory-connector",
		Name:        "Inventory Connector",
		Version:     "1.0.0",
		Entrypoint:  "./connector",
		Hooks:       []ManifestHook{{Event: "stock.adjusted", Action: "erp.stock.sync"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	bus := NewEventBus(db)
	ch, err := bus.Subscribe(ctx, manifest.ID, []string{"stock.adjusted"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	want := StockAdjustedEvent{
		ItemID:     "i-1",
		VariantID:  "v-2",
		SKU:        "SKU-1",
		DeltaQty:   -2.5, // a sale removed 2.5kg of a weighed good
		NewQty:     7.5,
		Reason:     "sale",
		Location:   "loc-main",
		AdjustedAt: time.Now().UTC(),
	}
	if _, err := bus.PublishStockAdjusted(ctx, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "stock.adjusted" {
			t.Fatalf("event type = %q", ev.Type)
		}
		var got StockAdjustedEvent
		if err := json.Unmarshal(ev.Payload, &got); err != nil {
			t.Fatalf("connector cannot decode payload: %v", err)
		}
		if got.ItemID != "i-1" || got.VariantID != "v-2" || got.SKU != "SKU-1" {
			t.Fatalf("identity lost: %+v", got)
		}
		if got.DeltaQty != -2.5 {
			t.Fatalf("signed decimal delta lost: %+v", got)
		}
		if got.Reason != "sale" || got.Location != "loc-main" {
			t.Fatalf("reason/location lost: %+v", got)
		}
		if got.NewQty != 7.5 {
			t.Fatalf("new_qty lost: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connector never received stock.adjusted")
	}
}
