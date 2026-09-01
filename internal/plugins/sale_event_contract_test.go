package plugins

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The sale.completed payload is the stable contract every ERP connector plugin
// (SAP, Dynamics/LS Central, generic webhook) depends on (ADR-0014). This test
// pins the wire shape: a subscriber must receive the full sale — receipt
// number, decimal quantities, per-line SKU/tax, and every payment — so a
// change that drops or renames a field is caught before it breaks connectors.
func TestSaleCompletedEvent_ConnectorContract(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)
	ctx := context.Background()

	// A realistic connector: an integration plugin hooking sale.completed.
	manifest := &Manifest{
		ID:          "com.example.erp-connector",
		Name:        "ERP Connector",
		Version:     "1.0.0",
		Entrypoint:  "./connector",
		Hooks:       []ManifestHook{{Event: "sale.completed", Action: "erp.push"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant permission: %v", err)
	}

	bus := NewEventBus(db)
	ch, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	want := SaleCompletedEvent{
		SaleID:        "sale-1",
		ReceiptNo:     "R-000123",
		SaleType:      "sale",
		OrderType:     "mixed", // ADR-0073: derived sale summary (additive field)
		Currency:      "AED",
		SubtotalCents: 10000,
		DiscountCents: 500,
		TaxCents:      475,
		TotalCents:    9975,
		CustomerID:    "cust-9",
		CashierID:     "u-2",
		PaymentMethod: "card",
		Payments: []SalePayment{
			{Method: "card", AmountCents: 9975, Reference: "AUTH-42"},
		},
		LineItems: []SaleLineItem{
			{ItemID: "i-1", SKU: "SKU-1", Name: "Rice 5kg", Quantity: 2.5,
				UnitPriceCents: 4000, TaxRateBP: 500, TaxCents: 475, TotalCents: 10000,
				OrderType: "takeaway"}, // ADR-0073: per-line mode (additive field)
		},
		CompletedAt: time.Now().UTC(),
	}
	if _, err := bus.PublishSaleCompleted(ctx, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != "sale.completed" {
			t.Fatalf("event type = %q", ev.Type)
		}
		var got SaleCompletedEvent
		if err := json.Unmarshal(ev.Payload, &got); err != nil {
			t.Fatalf("connector cannot decode payload: %v", err)
		}
		if got.ReceiptNo != "R-000123" || got.Currency != "AED" || got.TotalCents != 9975 {
			t.Fatalf("header lost: %+v", got)
		}
		if len(got.LineItems) != 1 || got.LineItems[0].SKU != "SKU-1" || got.LineItems[0].Quantity != 2.5 {
			t.Fatalf("line item lost (SKU/decimal qty): %+v", got.LineItems)
		}
		if got.OrderType != "mixed" || got.LineItems[0].OrderType != "takeaway" {
			t.Fatalf("ADR-0073 order types lost: header=%q line=%q", got.OrderType, got.LineItems[0].OrderType)
		}
		if len(got.Payments) != 1 || got.Payments[0].Method != "card" || got.Payments[0].AmountCents != 9975 {
			t.Fatalf("payment lost: %+v", got.Payments)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("connector never received sale.completed")
	}
}
