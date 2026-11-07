package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5758: POST /api/pos/no-sale publishes the neutral
// till.drawer.no_sale event (contract: ut-docs
// reference/contracts/till-drawer-no-sale.md) once the record has committed.

// subscribeNoSaleEvents registers an in-process channel subscriber. With
// grant=false the plugin lacks events:receive.
func subscribeNoSaleEvents(t *testing.T, dp *common.Deps, pluginID string, grant bool) <-chan plugins.Event {
	t.Helper()
	seedFiscalSignHookPlugin(t, dp, pluginID, tillDrawerNoSaleEvent, true)
	if !grant {
		if _, err := dp.Db.Exec(`DELETE FROM plugin_permissions WHERE plugin_id = ?`, pluginID); err != nil {
			t.Fatal(err)
		}
	}
	bus := plugins.SharedBus(dp.Db)
	ch, err := bus.Subscribe(context.Background(), pluginID, []string{tillDrawerNoSaleEvent})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(bus.ResetSubscribers)
	return ch
}

func eventPublishedRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='event_published' AND data_json LIKE '%event_type=till.drawer.no_sale,%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func recvNoSaleEvent(t *testing.T, ch <-chan plugins.Event) map[string]any {
	t.Helper()
	select {
	case ev := <-ch:
		var raw map[string]any
		if err := json.Unmarshal(ev.Payload, &raw); err != nil {
			t.Fatal(err)
		}
		return raw
	case <-time.After(2 * time.Second):
		t.Fatal("no till.drawer.no_sale event received")
		return nil
	}
}

func assertNoNoSaleEvent(t *testing.T, ch <-chan plugins.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected till.drawer.no_sale event: %s", ev.Payload)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestTillDrawerNoSale_ZeroSubscriberAllocatesNothing(t *testing.T) {
	_, dp, _ := newNoSaleTestDeps(t, "device")
	plugins.SharedBus(dp.Db).ResetSubscribers()
	ev := data.NoSaleEvent{CreatedAt: "2026-01-01T00:00:00Z", RegisterID: "r", ActorID: "u"}
	allocs := testing.AllocsPerRun(100, func() {
		dispatchTillDrawerNoSale(context.Background(), dp, "id-1", ev)
	})
	if allocs != 0 {
		t.Fatalf("zero-subscriber fast path must not allocate, got %v allocs/op", allocs)
	}
}

func TestNoSale_NoSubscriberWritesNoEventPublishedAudit(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	plugins.SharedBus(dp.Db).ResetSubscribers()
	old := publishTillDrawerNoSale
	publishTillDrawerNoSale = func(context.Context, *plugins.EventBus, plugins.TillDrawerNoSalePayload) (string, error) {
		t.Fatal("Publish must not be called with no subscriber")
		return "", nil
	}
	t.Cleanup(func() { publishTillDrawerNoSale = old })

	rec := postNoSale(t, mux, manager3134, `{}`)
	if id, _ := decodeNoSaleEnvelope(t, rec); rec.Code != http.StatusOK || id == "" {
		t.Fatalf("want 200 with id, got %d %s", rec.Code, rec.Body.String())
	}
	if n := eventPublishedRows(t, dp.Db); n != 0 {
		t.Fatalf("event_published rows = %d, want 0", n)
	}
}

func TestNoSale_PublishesOneEventAfterCommit_ExactPayload(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)

	rec := postNoSale(t, mux, manager3134, `{"reason":"till float check"}`)
	id, _ := decodeNoSaleEnvelope(t, rec)
	if rec.Code != http.StatusOK || id == "" {
		t.Fatalf("want 200 with id, got %d %s", rec.Code, rec.Body.String())
	}
	raw := recvNoSaleEvent(t, ch)
	assertNoNoSaleEvent(t, ch)

	if len(raw) != 6 {
		t.Fatalf("payload keys = %v, want exactly 6", raw)
	}
	if _, has := raw["reason"]; has {
		t.Fatal("payload must never carry reason")
	}
	for _, k := range []string{"no_sale_id", "register_id", "till_id", "actor_id", "approver_id", "created_at"} {
		if _, ok := raw[k].(string); !ok {
			t.Fatalf("key %q missing or not a string: %v", k, raw)
		}
	}
	if raw["no_sale_id"] != id || raw["register_id"] != "reg-ns" || raw["till_id"] != "" ||
		raw["actor_id"] != "mgr1" || raw["approver_id"] != "" {
		t.Fatalf("unexpected payload %v (id %s)", raw, id)
	}
	// The row exists (post-commit) and its created_at is byte-equal.
	var created string
	if err := dp.Db.QueryRow(`SELECT created_at FROM no_sale_events WHERE id = ?`, raw["no_sale_id"]).Scan(&created); err != nil {
		t.Fatalf("row not found by no_sale_id: %v", err)
	}
	if raw["created_at"] != created {
		t.Fatalf("created_at %v != row %q", raw["created_at"], created)
	}
	if _, err := time.Parse(time.RFC3339, created); err != nil {
		t.Fatalf("created_at not RFC 3339: %v", err)
	}
}

func TestNoSale_ElevatedEventCarriesApprover(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)
	rec := postNoSale(t, mux, cashier3134, `{"manager_pin":"`+perm3134MgrPIN+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	raw := recvNoSaleEvent(t, ch)
	if raw["actor_id"] != "cashier1" || raw["approver_id"] != "mgr1" {
		t.Fatalf("actor/approver = %v/%v, want cashier1/mgr1", raw["actor_id"], raw["approver_id"])
	}
}

func TestNoSale_RecordFailurePublishesNothing(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)
	if _, err := dp.Db.Exec(`CREATE TRIGGER no_sale_audit_fail BEFORE INSERT ON audit_log WHEN NEW.action='no_sale' BEGIN SELECT RAISE(ABORT,'x'); END`); err != nil {
		t.Fatal(err)
	}
	rec := postNoSale(t, mux, manager3134, `{}`)
	if _, code := decodeNoSaleEnvelope(t, rec); rec.Code != http.StatusInternalServerError || code != "not_recorded" {
		t.Fatalf("want 500 not_recorded, got %d %s", rec.Code, rec.Body.String())
	}
	if rows := noSaleRows(t, dp.Db); len(rows) != 0 {
		t.Fatalf("tx must roll back, got rows %+v", rows)
	}
	assertNoNoSaleEvent(t, ch)
	if n := eventPublishedRows(t, dp.Db); n != 0 {
		t.Fatalf("event_published rows = %d, want 0", n)
	}
}

func TestNoSale_RefusalsAndKickFailuresPublishNothing(t *testing.T) {
	cases := []struct {
		name, printer, body string
		user                auth.User
		want                int
	}{
		{"no pin", "device", `{}`, cashier3134, http.StatusForbidden},
		{"no printer", "", `{}`, manager3134, http.StatusConflict},
		{"system printer", "system", `{}`, manager3134, http.StatusConflict},
		{"kick fails", "broken", `{}`, manager3134, http.StatusServiceUnavailable},
		{"bad body", "device", `[`, manager3134, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp, _ := newNoSaleTestDeps(t, tc.printer)
			ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)
			if rec := postNoSale(t, mux, tc.user, tc.body); rec.Code != tc.want {
				t.Fatalf("want %d, got %d %s", tc.want, rec.Code, rec.Body.String())
			}
			assertNoNoSaleEvent(t, ch)
			if n := eventPublishedRows(t, dp.Db); n != 0 {
				t.Fatalf("event_published rows = %d, want 0", n)
			}
		})
	}
}

func TestNoSale_SubscriberFailureKeepsOK(t *testing.T) {
	stub := func(t *testing.T, f func(context.Context, *plugins.EventBus, plugins.TillDrawerNoSalePayload) (string, error)) {
		old := publishTillDrawerNoSale
		publishTillDrawerNoSale = f
		t.Cleanup(func() { publishTillDrawerNoSale = old })
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, dp *common.Deps)
	}{
		{"no events:receive grant", func(t *testing.T, dp *common.Deps) { subscribeNoSaleEvents(t, dp, "com.test.denied", false) }},
		{"full channel", func(t *testing.T, dp *common.Deps) {
			subscribeNoSaleEvents(t, dp, "com.test.full", true)
			bus := plugins.SharedBus(dp.Db)
			for i := 0; i < 150; i++ {
				_, _ = bus.PublishTillDrawerNoSale(context.Background(), plugins.TillDrawerNoSalePayload{NoSaleID: "fill"})
			}
		}},
		{"publish error", func(t *testing.T, dp *common.Deps) {
			subscribeNoSaleEvents(t, dp, "com.test.err", true)
			stub(t, func(context.Context, *plugins.EventBus, plugins.TillDrawerNoSalePayload) (string, error) {
				return "", errors.New("boom")
			})
		}},
		{"publish panic", func(t *testing.T, dp *common.Deps) {
			subscribeNoSaleEvents(t, dp, "com.test.panic", true)
			stub(t, func(context.Context, *plugins.EventBus, plugins.TillDrawerNoSalePayload) (string, error) {
				panic("boom")
			})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp, _ := newNoSaleTestDeps(t, "device")
			tc.setup(t, dp)
			rec := postNoSale(t, mux, manager3134, `{}`)
			id, _ := decodeNoSaleEnvelope(t, rec)
			if rec.Code != http.StatusOK || id == "" {
				t.Fatalf("want 200 with id, got %d %s", rec.Code, rec.Body.String())
			}
			if rows := noSaleRows(t, dp.Db); len(rows) != 1 || rows[0].id != id {
				t.Fatalf("row not persisted: %+v", rows)
			}
			if a := noSaleAudit(t, dp.Db); len(a) != 1 {
				t.Fatalf("audit rows = %d, want 1", len(a))
			}
		})
	}
}

func TestNoSale_TwoOpensTwoDistinctEvents(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)
	for i := 0; i < 2; i++ {
		if rec := postNoSale(t, mux, manager3134, `{}`); rec.Code != http.StatusOK {
			t.Fatalf("open %d: %d", i, rec.Code)
		}
	}
	a, b := recvNoSaleEvent(t, ch), recvNoSaleEvent(t, ch)
	if a["no_sale_id"] == b["no_sale_id"] || a["no_sale_id"] == "" {
		t.Fatalf("want two distinct ids, got %v / %v", a["no_sale_id"], b["no_sale_id"])
	}
	assertNoNoSaleEvent(t, ch)
}

func TestNoSale_UnresolvedRegisterStillPublishes(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	setSettings(t, dp, map[string]string{pos.SettingsKeyTillRegisterID: ""})
	ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)
	if rec := postNoSale(t, mux, manager3134, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	raw := recvNoSaleEvent(t, ch)
	rows := noSaleRows(t, dp.Db)
	if len(rows) != 1 || raw["register_id"] != rows[0].register {
		t.Fatalf("event register_id %v must equal row %+v", raw["register_id"], rows)
	}
}

// AC3 (Tester, ut-docs#5758): at the moment core publishes, the row is
// already committed — a fresh connection finds it by no_sale_id. A publish
// inside the tx fails here fast (row invisible / pool busy) instead of
// deadlocking the package until the go test timeout.
func TestNoSale_PublishSeesCommittedRow(t *testing.T) {
	mux, dp, _ := newNoSaleTestDeps(t, "device")
	subscribeNoSaleEvents(t, dp, "com.test.signer", true)
	var seen, calls int
	old := publishTillDrawerNoSale
	publishTillDrawerNoSale = func(ctx context.Context, _ *plugins.EventBus, ev plugins.TillDrawerNoSalePayload) (string, error) {
		calls++
		qctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var n int
		if err := dp.Db.QueryRowContext(qctx, `SELECT COUNT(*) FROM no_sale_events WHERE id = ?`, ev.NoSaleID).Scan(&n); err == nil {
			seen = n
		}
		return "", nil
	}
	t.Cleanup(func() { publishTillDrawerNoSale = old })
	if rec := postNoSale(t, mux, manager3134, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	if calls != 1 || seen != 1 {
		t.Fatalf("publish calls=%d, committed row visible at publish=%d; want 1/1", calls, seen)
	}
}

// Review (ut-docs#5758): the record is committed and the drawer open before
// dispatch, so a request context cancelled by a client hanging up must not
// lose the event.
func TestTillDrawerNoSale_CancelledRequestContextStillDelivers(t *testing.T) {
	_, dp, _ := newNoSaleTestDeps(t, "device")
	ch := subscribeNoSaleEvents(t, dp, "com.test.signer", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dispatchTillDrawerNoSale(ctx, dp, "id-1", data.NoSaleEvent{CreatedAt: "2026-01-01T00:00:00Z"})
	if raw := recvNoSaleEvent(t, ch); raw["no_sale_id"] != "id-1" {
		t.Fatalf("payload %v", raw)
	}
}
