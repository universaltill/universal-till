package plugins

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ut-docs#791: sale.completed's card-present reconciliation fields (masked
// PAN, auth code, terminal/trace ID — ut-docs#543) must only reach a
// subscriber holding the payments:reconciliation permission; every other
// sale.completed subscriber still gets the event (line items, totals, the
// non-card payment method) with those four fields absent.

func newReconciliationTestSale() SaleCompletedEvent {
	return SaleCompletedEvent{
		SaleID:     "sale-recon-1",
		TotalCents: 5500,
		Payments: []SalePayment{
			{
				Method:      "card",
				AmountCents: 5500,
				Reference:   "tx-ref-1",
				MaskedPAN:   "411111******1111",
				AuthCode:    "AUTH123",
				TerminalID:  "term-9",
				TraceID:     "trace-9",
			},
		},
		CompletedAt: time.Now(),
	}
}

func TestEventBus_SaleCompleted_RedactsCardFieldsWithoutPermission(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.erp-only",
		Name:       "ERP connector (no card access)",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "sale.completed", Action: "erp.sync"},
		},
		// Deliberately does NOT declare payments:reconciliation.
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive: %v", err)
	}

	bus := NewEventBus(db)
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if _, err := bus.PublishSaleCompleted(ctx, newReconciliationTestSale()); err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	select {
	case event := <-eventChan:
		var got SaleCompletedEvent
		if err := json.Unmarshal(event.Payload, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if len(got.Payments) != 1 {
			t.Fatalf("expected 1 payment to survive redaction, got %d", len(got.Payments))
		}
		p := got.Payments[0]
		// Non-card-present fields must be untouched.
		if p.Method != "card" || p.AmountCents != 5500 || p.Reference != "tx-ref-1" {
			t.Errorf("redaction altered non-card-present fields: %+v", p)
		}
		// Card-present reconciliation fields must be gone.
		if p.MaskedPAN != "" || p.AuthCode != "" || p.TerminalID != "" || p.TraceID != "" {
			t.Errorf("expected card-present fields redacted, got %+v", p)
		}
		// omitempty: redacted fields must be genuinely ABSENT from the
		// wire payload, not present-but-empty — a plugin author must not
		// be able to distinguish "no card data" from "redacted" here any
		// more than the sales/stock export ledgers allow (ut-docs#228).
		raw := string(event.Payload)
		for _, key := range []string{"masked_pan", "auth_code", "terminal_id", "trace_id"} {
			if containsJSONKey(raw, key) {
				t.Errorf("expected redacted payload to omit %q entirely, got: %s", key, raw)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_SaleCompleted_FullPayloadWithPermission(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.card-reconciler",
		Name:       "Card reconciliation connector",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "sale.completed", Action: "reconcile.sync"},
		},
		Permissions: []string{"events:receive", "payments:reconciliation"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "payments:reconciliation"); err != nil {
		t.Fatalf("grant payments:reconciliation: %v", err)
	}

	bus := NewEventBus(db)
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if _, err := bus.PublishSaleCompleted(ctx, newReconciliationTestSale()); err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	select {
	case event := <-eventChan:
		var got SaleCompletedEvent
		if err := json.Unmarshal(event.Payload, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if len(got.Payments) != 1 {
			t.Fatalf("expected 1 payment, got %d", len(got.Payments))
		}
		p := got.Payments[0]
		if p.MaskedPAN != "411111******1111" || p.AuthCode != "AUTH123" || p.TerminalID != "term-9" || p.TraceID != "trace-9" {
			t.Errorf("expected full card-present fields for a granted subscriber, got %+v", p)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_SaleCompleted_DeclaredButNotGrantedStillRedacted(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.declared-not-granted",
		Name:       "Declared but not yet granted",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "sale.completed", Action: "reconcile.sync"},
		},
		// Declares the permission (so a manual/sideloaded import shows it
		// on the merchant's approval screen) but only events:receive is
		// actually granted here — payments:reconciliation stays row-
		// present/granted=0 until a merchant explicitly approves it.
		Permissions: []string{"events:receive", "payments:reconciliation"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive: %v", err)
	}

	bus := NewEventBus(db)
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if _, err := bus.PublishSaleCompleted(ctx, newReconciliationTestSale()); err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	select {
	case event := <-eventChan:
		var got SaleCompletedEvent
		if err := json.Unmarshal(event.Payload, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if got.Payments[0].MaskedPAN != "" || got.Payments[0].TerminalID != "" {
			t.Errorf("declared-but-ungranted permission must still redact, got %+v", got.Payments[0])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_NonSaleEvent_UnaffectedByReconciliationGate(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:         "com.test.stock-sync",
		Name:       "Stock connector",
		Version:    "1.0.0",
		Entrypoint: "./test",
		Hooks: []ManifestHook{
			{Event: "stock.adjusted", Action: "stock.sync"},
		},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive: %v", err)
	}

	bus := NewEventBus(db)
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"stock.adjusted"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	ev := StockAdjustedEvent{ItemID: "itm-1", DeltaQty: -1, Reason: "sale", AdjustedAt: time.Now()}
	if _, err := bus.PublishStockAdjusted(ctx, ev); err != nil {
		t.Fatalf("PublishStockAdjusted failed: %v", err)
	}

	select {
	case event := <-eventChan:
		var got StockAdjustedEvent
		if err := json.Unmarshal(event.Payload, &got); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if got.ItemID != "itm-1" || got.DeltaQty != -1 {
			t.Errorf("stock.adjusted payload unexpectedly altered: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

// TestEventBus_SaleCompleted_MixedSubscribers_NoAliasing publishes to two
// subscribers in the SAME dispatch loop — one without payments:reconciliation,
// one with it — the combination that would catch a shared-backing-array
// aliasing bug if redactCardPresentFields (or publish()'s per-subscriber
// subEvent handling) ever mutated shared state instead of building an
// independent redacted copy: the granted subscriber's payload must still
// carry the full card-present fields even though an ungranted subscriber's
// redacted copy was built and dispatched first.
func TestEventBus_SaleCompleted_MixedSubscribers_NoAliasing(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	ungranted := &Manifest{
		ID:          "com.test.mixed-ungranted",
		Name:        "ERP connector (no card access)",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Hooks:       []ManifestHook{{Event: "sale.completed", Action: "erp.sync"}},
		Permissions: []string{"events:receive"},
	}
	granted := &Manifest{
		ID:          "com.test.mixed-granted",
		Name:        "Card reconciliation connector",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Hooks:       []ManifestHook{{Event: "sale.completed", Action: "reconcile.sync"}},
		Permissions: []string{"events:receive", "payments:reconciliation"},
	}
	for _, m := range []*Manifest{ungranted, granted} {
		if err := PersistManifest(ctx, db, m, InstallOptions{}); err != nil {
			t.Fatalf("persist manifest %s: %v", m.ID, err)
		}
	}
	if err := GrantPermission(ctx, db, ungranted.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive (ungranted): %v", err)
	}
	if err := GrantPermission(ctx, db, granted.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive (granted): %v", err)
	}
	if err := GrantPermission(ctx, db, granted.ID, "payments:reconciliation"); err != nil {
		t.Fatalf("grant payments:reconciliation: %v", err)
	}

	bus := NewEventBus(db)
	// Subscribe order matters for this test: the ungranted subscriber's
	// redacted payload is built and dispatched first, then the granted
	// subscriber's — the direction an aliasing bug would actually surface.
	ungrantedChan, err := bus.Subscribe(ctx, ungranted.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe ungranted failed: %v", err)
	}
	grantedChan, err := bus.Subscribe(ctx, granted.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe granted failed: %v", err)
	}

	if _, err := bus.PublishSaleCompleted(ctx, newReconciliationTestSale()); err != nil {
		t.Fatalf("PublishSaleCompleted failed: %v", err)
	}

	timeout := time.After(time.Second)
	var gotUngranted, gotGranted SaleCompletedEvent
	for i := 0; i < 2; i++ {
		select {
		case event := <-ungrantedChan:
			if err := json.Unmarshal(event.Payload, &gotUngranted); err != nil {
				t.Fatalf("unmarshal ungranted payload: %v", err)
			}
		case event := <-grantedChan:
			if err := json.Unmarshal(event.Payload, &gotGranted); err != nil {
				t.Fatalf("unmarshal granted payload: %v", err)
			}
		case <-timeout:
			t.Fatal("timeout waiting for events")
		}
	}

	if p := gotUngranted.Payments[0]; p.MaskedPAN != "" || p.TerminalID != "" {
		t.Errorf("ungranted subscriber got card-present fields: %+v", p)
	}
	if p := gotGranted.Payments[0]; p.MaskedPAN != "411111******1111" || p.TerminalID != "term-9" {
		t.Errorf("granted subscriber's payload was corrupted by the other subscriber's redaction: %+v", p)
	}
}

// TestEventBus_SaleCompleted_ReconciliationDenialNotAudited locks in the
// review fix: "not granted payments:reconciliation" is the expected,
// permanent steady state for most sale.completed subscribers (plain
// ERP/accounting connectors never declare it), not an exceptional access
// attempt — so, unlike every other permission, it must NOT write an
// audit_log "permission_denied" row on every single sale. Publishing
// several sales to one permanently-ungranted subscriber must add zero such
// rows for this permission (events:receive's own successful-dispatch
// audit rows are unaffected and not asserted against here).
func TestEventBus_SaleCompleted_ReconciliationDenialNotAudited(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	setupAuditLog(t, db)

	ctx := context.Background()

	manifest := &Manifest{
		ID:          "com.test.no-audit-spam",
		Name:        "ERP connector (no card access)",
		Version:     "1.0.0",
		Entrypoint:  "./test",
		Hooks:       []ManifestHook{{Event: "sale.completed", Action: "erp.sync"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant events:receive: %v", err)
	}

	bus := NewEventBus(db)
	eventChan, err := bus.Subscribe(ctx, manifest.ID, []string{"sale.completed"})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	const numSales = 5
	for i := 0; i < numSales; i++ {
		if _, err := bus.PublishSaleCompleted(ctx, newReconciliationTestSale()); err != nil {
			t.Fatalf("PublishSaleCompleted #%d failed: %v", i, err)
		}
		select {
		case <-eventChan:
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for event #%d", i)
		}
	}

	var denials int
	row := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_log
		WHERE action = 'permission_denied'
		AND entity_type = 'plugin'
		AND entity_id = ?
		AND data_json LIKE '%payments:reconciliation%'
	`, manifest.ID)
	if err := row.Scan(&denials); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if denials != 0 {
		t.Errorf("expected 0 payments:reconciliation audit_log denial rows after %d sales, got %d", numSales, denials)
	}
}

// containsJSONKey is a light substring check for `"key":` in a compact
// JSON document — good enough to assert omitempty actually dropped the
// field rather than serializing it as "".
func containsJSONKey(raw, key string) bool {
	needle := `"` + key + `":`
	for i := 0; i+len(needle) <= len(raw); i++ {
		if raw[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
