package plugins

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#5770: the shift.opened / shift.closed contract
// (ut-docs reference/contracts/shift-events.md).

func subscribeShiftEvent(t *testing.T, event string) (*EventBus, <-chan Event) {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	setupAuditLog(t, db)
	ctx := context.Background()
	manifest := &Manifest{
		ID: "com.example.accounting", Name: "Accounting", Version: "1.0.0", Entrypoint: "./s",
		Hooks:       []ManifestHook{{Event: event, Action: "acct.record"}},
		Permissions: []string{"events:receive"},
	}
	if err := PersistManifest(ctx, db, manifest, InstallOptions{}); err != nil {
		t.Fatalf("persist manifest: %v", err)
	}
	if err := GrantPermission(ctx, db, manifest.ID, "events:receive"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	bus := NewEventBus(db)
	ch, err := bus.Subscribe(ctx, manifest.ID, []string{event})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return bus, ch
}

func recvShiftPayload(t *testing.T, ch <-chan Event, wantType string) (map[string]any, []byte) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Type != wantType {
			t.Fatalf("type = %q, want %q", ev.Type, wantType)
		}
		var raw map[string]any
		if err := json.Unmarshal(ev.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		return raw, ev.Payload
	case <-time.After(2 * time.Second):
		t.Fatalf("subscriber never received %s", wantType)
		return nil, nil
	}
}

func assertKeys(t *testing.T, raw map[string]any, want []string) {
	t.Helper()
	if len(raw) != len(want) {
		t.Fatalf("payload keys = %v, want exactly %v", raw, want)
	}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Fatalf("key %q missing from %v", k, raw)
		}
	}
	for _, banned := range []string{"note", "skim_reason", "count_protocol"} {
		if _, ok := raw[banned]; ok {
			t.Fatalf("payload must never carry %q", banned)
		}
	}
}

func assertIntegral(t *testing.T, raw map[string]any, key string, want float64) {
	t.Helper()
	f, ok := raw[key].(float64)
	if !ok || f != want {
		t.Fatalf("%s = %v (%T), want JSON number %v", key, raw[key], raw[key], want)
	}
}

func TestShiftEvents_RegistrationAndDataClasses(t *testing.T) {
	if ShiftOpenedEvent != "shift.opened" || ShiftClosedEvent != "shift.closed" {
		t.Fatalf("names = %q / %q", ShiftOpenedEvent, ShiftClosedEvent)
	}
	bus := NewEventBus(nil)
	for _, name := range []string{ShiftOpenedEvent, ShiftClosedEvent} {
		if _, ok := bus.eventModes[name]; !ok {
			t.Errorf("%s is not registered in eventModes", name)
		}
		if m := bus.GetEventMode(name); m != NonBlocking {
			t.Errorf("%s mode = %v, want NonBlocking", name, m)
		}
		classes, ok := EventDataClasses(name)
		if !ok {
			t.Fatalf("%s has no data-class entry (fails closed for ai.agent)", name)
		}
		got := slices.Clone(classes)
		slices.Sort(got)
		if !slices.Equal(got, []string{"sales", "staff"}) {
			t.Errorf("%s data classes = %v, want {sales, staff}", name, classes)
		}
	}
	for _, retired := range []string{"sale.viewed", "inventory.adjusted", "report.generated"} {
		if _, ok := bus.eventModes[retired]; ok {
			t.Errorf("retired name %q is still in eventModes (ut-docs#5770)", retired)
		}
	}
}

func TestShiftOpenedPayload_WireShape(t *testing.T) {
	bus, ch := subscribeShiftEvent(t, ShiftOpenedEvent)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := bus.PublishShiftOpened(context.Background(), ShiftOpenedPayload{
		ShiftID: "s-1", RegisterID: "reg-1", CashierID: "u1", Currency: "GBP",
		OpeningCash: money.FromMinor(10000), OpenedAt: now,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	raw, _ := recvShiftPayload(t, ch, ShiftOpenedEvent)
	assertKeys(t, raw, []string{"shift_id", "register_id", "cashier_id", "currency", "opening_cash", "opened_at"})
	assertIntegral(t, raw, "opening_cash", 10000)
	if raw["shift_id"] != "s-1" || raw["register_id"] != "reg-1" || raw["cashier_id"] != "u1" ||
		raw["currency"] != "GBP" || raw["opened_at"] != now {
		t.Fatalf("unexpected values: %v", raw)
	}
}

func TestShiftClosedPayload_WireShape(t *testing.T) {
	bus, ch := subscribeShiftEvent(t, ShiftClosedEvent)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := bus.PublishShiftClosed(context.Background(), ShiftClosedPayload{
		ShiftID: "s-1", RegisterID: "reg-1", CashierID: "u1", Currency: "",
		OpenedAt: now, ClosedAt: now,
		OpeningCash: money.FromMinor(10000), ClosingCash: money.FromMinor(9000),
		ExpectedCash: money.FromMinor(9500), Variance: money.FromMinor(-500),
		Skim: money.FromMinor(4000), NewFloat: money.FromMinor(5000),
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	raw, _ := recvShiftPayload(t, ch, ShiftClosedEvent)
	assertKeys(t, raw, []string{
		"shift_id", "register_id", "cashier_id", "currency", "opened_at", "closed_at",
		"opening_cash", "closing_cash", "expected_cash", "variance", "skim", "new_float",
	})
	for k, v := range map[string]float64{
		"opening_cash": 10000, "closing_cash": 9000, "expected_cash": 9500,
		"variance": -500, "skim": 4000, "new_float": 5000,
	} {
		assertIntegral(t, raw, k, v)
	}
	if raw["currency"] != "" || raw["closed_at"] != now {
		t.Fatalf("unexpected values: %v", raw)
	}
}
