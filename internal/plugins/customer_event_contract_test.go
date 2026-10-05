package plugins

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The customer.erased payload (ut-docs#3435) is the contract loyalty, CRM
// and integration plugins rely on to delete their own copy of a customer the
// till erased (GDPR). It carries the customer id and nothing else: the name
// and contact data are exactly what was just erased, so they must never ride
// along. This test pins that wire shape.
func TestCustomerErasedEvent_ConnectorContract(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.example.loyalty",
		Name:        "Loyalty",
		Version:     "1.0.0",
		Entrypoint:  "./loyalty",
		Hooks:       []ManifestHook{{Event: "customer.erased", Action: "loyalty.forget"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	bus := NewEventBus(db)
	if mode := bus.GetEventMode("customer.erased"); mode != NonBlocking {
		t.Fatalf("customer.erased must never block the erasure: mode=%v", mode)
	}
	ch, err := bus.Subscribe(ctx, manifest.ID, []string{"customer.erased"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if _, err := bus.PublishCustomerErased(ctx, CustomerErasedEvent{CustomerID: "cust-42"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "customer.erased" {
			t.Fatalf("event type = %q", ev.Type)
		}
		var got CustomerErasedEvent
		if err := json.Unmarshal(ev.Payload, &got); err != nil {
			t.Fatalf("plugin cannot decode payload: %v", err)
		}
		if got.CustomerID != "cust-42" {
			t.Fatalf("customer_id lost: %+v", got)
		}
		var raw map[string]any
		if err := json.Unmarshal(ev.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 1 || raw["customer_id"] != "cust-42" {
			t.Fatalf("payload must be {customer_id} only, got %s", ev.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plugin never received customer.erased")
	}
}
