package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0153 amendment 2026-10-15 (ut-docs#4147): an ai.agent plugin gets an
// event only when every non-shop data class the payload carries is granted
// as ai.send.<class>. Driven through a real wasip1 guest (testdata/
// view_guest records "results" in plugin storage whenever it runs) loaded
// by a real Sync, so the cached "declares ai.agent" fact comes from the
// installed manifest the way it does on a till.

const evAgentID = "com.test.evagent"

// evRig installs pluginID with the given manifest permissions (all declared
// and granted except those in revoked), events:receive and storage, an
// active hook per event in hooks, the view guest as its module, and Syncs a
// fresh runtime over it.
// Review MINOR: a lost manifest.json (InstalledManifest ok=false) must not
// switch an ai.agent plugin's gate off — the declared permission row
// decides; a plugin without that row stays unchecked.
func TestEventDataClass_MissingManifestFallsBackToDeclaredRow(t *testing.T) {
	ctx := context.Background()
	const agent, plain = "com.test.evnomanifest", "com.test.evnomanifestplain"
	d := streamDB(t, agent, "events:receive")
	grantPerm(t, d, agent, AIAgentPermission)
	if g, err := installedEventGate(ctx, d, agent); err != nil || g != eventGateAIAgent {
		t.Fatalf("gate = %v, %v; want eventGateAIAgent", g, err)
	}
	d2 := streamDB(t, plain, "events:receive")
	if g, err := installedEventGate(ctx, d2, plain); err != nil || g != eventGateNone {
		t.Fatalf("gate = %v, %v; want eventGateNone", g, err)
	}
}

func evRig(t *testing.T, pluginID string, declared, revoked, hooks []string) (*WasmRuntime, *sql.DB) {
	t.Helper()
	raw := evGuestBytes(t)
	withInstalledManifest(t, pluginID, aiAgentManifest(pluginID, declared))
	if err := writeFileWithParents(paths.Plugins(pluginID, "1.0.0", "plugin.wasm"), raw); err != nil {
		t.Fatal(err)
	}
	d := streamDB(t, pluginID, "events:receive")
	repo := data.NewPluginRepo(d)
	ctx := context.Background()
	for _, p := range declared {
		grantPerm(t, d, pluginID, p)
		if slices.Contains(revoked, p) {
			if err := repo.SetPermission(ctx, pluginID, p, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, ev := range hooks {
		if _, err := d.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, ?, 'guest.handle', 1)`,
			pluginID+"-evh"+string(rune('a'+i)), pluginID, ev); err != nil {
			t.Fatalf("seed hook: %v", err)
		}
	}
	w := NewWasmRuntime(paths.Plugins())
	w.Sync(ctx, d)
	t.Cleanup(func() {
		w.Close(context.Background())
		SharedBus(d).ResetSubscribers()
	})
	return w, d
}

// evGuest holds the view guest module, built once for these tests (each
// build costs seconds on a Pi-class runner).
var evGuest struct {
	once sync.Once
	raw  []byte
	err  error
}

func evGuestBytes(t *testing.T) []byte {
	t.Helper()
	evGuest.once.Do(func() {
		evGuest.raw, evGuest.err = os.ReadFile(buildViewGuest(t))
	})
	if evGuest.err != nil || len(evGuest.raw) == 0 {
		t.Fatalf("view guest unavailable: %v", evGuest.err)
	}
	return evGuest.raw
}

// guestRan reports whether the guest stored its results (and clears them).
func guestRan(t *testing.T, d *sql.DB, pluginID string) bool {
	t.Helper()
	_, err := data.NewPluginRepo(d).StorageGet(context.Background(), pluginID, "results")
	if errors.Is(err, data.ErrStorageNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	if _, err := d.Exec(`DELETE FROM plugin_storage WHERE plugin_id = ? AND key = 'results'`, pluginID); err != nil {
		t.Fatal(err)
	}
	return true
}

// denialRows returns the reason of every permission_denied row for pluginID
// naming permission, oldest first.
func denialRows(t *testing.T, d *sql.DB, pluginID, permission string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT COALESCE(data_json, '') FROM audit_log WHERE action = 'permission_denied' AND entity_id = ? ORDER BY rowid`, pluginID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var p map[string]any
		if json.Unmarshal([]byte(payload), &p) == nil && p["permission"] == permission {
			r, _ := p["reason"].(string)
			out = append(out, r)
		}
	}
	return out
}

func deliverEv(t *testing.T, w *WasmRuntime, pluginID string, ev Event) error {
	t.Helper()
	if ev.ID == "" {
		ev.ID = "ev-" + ev.Type
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	if len(ev.Payload) == 0 {
		ev.Payload = []byte(`{}`)
	}
	_, err := w.HandleEvent(context.Background(), pluginID, ev)
	return err
}

func saleEvent() SaleCompletedEvent {
	return SaleCompletedEvent{SaleID: "s1", ReceiptNo: "R1", SaleType: "sale", Currency: "GBP",
		TotalCents: 500, CustomerID: "cust-1", CashierID: "staff-1"}
}

// waitFor polls cond until it holds or 5 s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var allSaleClasses = []string{"ai.agent", "ai.send.sales", "ai.send.customers", "ai.send.staff"}

// Acceptance 1: ai.send.sales revoked → the bus-published sale.completed
// never reaches the guest, and a permission_denied row names it.
func TestEventDataClass_AIAgentSaleCompletedNeedsAISendSales(t *testing.T) {
	_, d := evRig(t, evAgentID, allSaleClasses, []string{"ai.send.sales"}, []string{"sale.completed"})
	if _, err := SharedBus(d).PublishSaleCompleted(context.Background(), saleEvent()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the ai.send.sales denial row", func() bool { return len(denialRows(t, d, evAgentID, "ai.send.sales")) > 0 })
	if guestRan(t, d, evAgentID) {
		t.Fatal("ai.agent plugin without ai.send.sales received sale.completed")
	}
	reason := denialRows(t, d, evAgentID, "ai.send.sales")[0]
	if !strings.Contains(reason, "sale.completed") {
		t.Fatalf("denial reason %q does not name sale.completed", reason)
	}
}

// Acceptance 2: with sales, customers and staff granted it is delivered.
func TestEventDataClass_AIAgentSaleCompletedDeliveredWhenAllGranted(t *testing.T) {
	_, d := evRig(t, evAgentID, allSaleClasses, nil, []string{"sale.completed"})
	if _, err := SharedBus(d).PublishSaleCompleted(context.Background(), saleEvent()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the guest to run", func() bool { return guestRan(t, d, evAgentID) })
	if n := countDenials(t, d, evAgentID); n != 0 {
		t.Fatalf("granted delivery audited %d denials", n)
	}
}

// Each class of sale.completed's set is needed, not just sales.
func TestEventDataClass_AIAgentSaleCompletedNeedsEveryClass(t *testing.T) {
	for _, missing := range []string{"ai.send.sales", "ai.send.customers", "ai.send.staff"} {
		t.Run(missing, func(t *testing.T) {
			w, d := evRig(t, evAgentID, allSaleClasses, []string{missing}, nil)
			err := deliverEv(t, w, evAgentID, Event{Type: "sale.completed"})
			if !errors.Is(err, ErrEventDataClassDenied) {
				t.Fatalf("err = %v, want ErrEventDataClassDenied", err)
			}
			if guestRan(t, d, evAgentID) {
				t.Fatalf("delivered without %s", missing)
			}
			if len(denialRows(t, d, evAgentID, missing)) != 1 {
				t.Fatalf("no permission_denied row for %s", missing)
			}
		})
	}
}

// Acceptance 3: a plugin without ai.agent is unaffected and audits nothing.
func TestEventDataClass_NonAIAgentUnaffected(t *testing.T) {
	const id = "com.test.evplain"
	_, d := evRig(t, id, nil, nil, []string{"sale.completed"})
	if _, err := SharedBus(d).PublishSaleCompleted(context.Background(), saleEvent()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, "the guest to run", func() bool { return guestRan(t, d, id) })
	if n := countDenials(t, d, id); n != 0 {
		t.Fatalf("non-ai.agent delivery audited %d denials", n)
	}
}

// Acceptance 4: the empty-set events and the plugin's own events need no
// ai.send.* grant.
func TestEventDataClass_AIAgentEmptySetAndOwnEvents(t *testing.T) {
	w, d := evRig(t, evAgentID, []string{"ai.agent"}, nil, nil)
	for _, ev := range []Event{
		{Type: "ui.view.ask"},
		{Type: "ui.action.ask"},
		{Type: "plugin.button.pressed"},
		{Type: "agent.action.result"},
		{Type: evAgentID + ".tick", Scheduled: true},
		{Type: evAgentID + ".done", Hop: 1, Publisher: evAgentID},
	} {
		if err := deliverEv(t, w, evAgentID, ev); err != nil {
			t.Fatalf("%s: %v", ev.Type, err)
		}
		if !guestRan(t, d, evAgentID) {
			t.Fatalf("%s did not reach the guest", ev.Type)
		}
	}
	if n := countDenials(t, d, evAgentID); n != 0 {
		t.Fatalf("allowed deliveries audited %d denials", n)
	}
}

// Acceptance 5: an event with no registry entry is refused (fail closed)
// and audited under ai.agent — including one published by another plugin
// whose id nests under this one's (its name starts with "<own id>." but it
// is not this plugin's own event).
func TestEventDataClass_AIAgentUnregisteredEventRefused(t *testing.T) {
	w, d := evRig(t, evAgentID, []string{"ai.agent", "ai.send.sales", "ai.send.catalog"}, nil, nil)
	for _, ev := range []Event{
		{Type: "tax.rate.ask"},
		{Type: "com.test.other.thing", Hop: 1, Publisher: "com.test.other"},
		{Type: evAgentID + ".sub.thing", Hop: 1, Publisher: evAgentID + ".sub"},
		// core-published under the plugin's prefix (a manifest-named payment
		// trigger / authorize hook): not its own, refused (review MAJOR)
		{Type: evAgentID + ".pay.authorize"},
	} {
		err := deliverEv(t, w, evAgentID, ev)
		if !errors.Is(err, ErrEventDataClassDenied) {
			t.Fatalf("%s: err = %v, want ErrEventDataClassDenied", ev.Type, err)
		}
		if guestRan(t, d, evAgentID) {
			t.Fatalf("%s reached the guest", ev.Type)
		}
	}
	rows := denialRows(t, d, evAgentID, AIAgentPermission)
	if len(rows) != 4 || !strings.Contains(rows[0], "tax.rate.ask") || !strings.Contains(rows[1], "com.test.other.thing") {
		t.Fatalf("ai.agent denial rows = %q, want one per refused event naming it", rows)
	}
}

// Acceptance 6: repeated refusals within the interval write one row; the
// next row after the interval carries the coalesced count.
func TestEventDataClass_DenialAuditCoalesced(t *testing.T) {
	w, d := evRig(t, evAgentID, allSaleClasses, []string{"ai.send.sales"}, nil)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	w.evDenials.now = clock.Now
	for range 5 {
		if err := deliverEv(t, w, evAgentID, Event{Type: "sale.completed"}); !errors.Is(err, ErrEventDataClassDenied) {
			t.Fatalf("err = %v", err)
		}
	}
	if rows := denialRows(t, d, evAgentID, "ai.send.sales"); len(rows) != 1 {
		t.Fatalf("%d denial rows within one interval, want 1: %q", len(rows), rows)
	}
	// Another event type is its own key.
	if err := deliverEv(t, w, evAgentID, Event{Type: "tax.rate.ask"}); !errors.Is(err, ErrEventDataClassDenied) {
		t.Fatalf("err = %v", err)
	}
	if rows := denialRows(t, d, evAgentID, AIAgentPermission); len(rows) != 1 {
		t.Fatalf("other event type rows = %q, want 1", rows)
	}
	clock.Advance(publishedAuditInterval)
	if err := deliverEv(t, w, evAgentID, Event{Type: "sale.completed"}); !errors.Is(err, ErrEventDataClassDenied) {
		t.Fatalf("err = %v", err)
	}
	rows := denialRows(t, d, evAgentID, "ai.send.sales")
	if len(rows) != 2 || !strings.Contains(rows[1], "coalesced=4") {
		t.Fatalf("rows after the interval = %q, want a second row with coalesced=4", rows)
	}
}

// Acceptance 7: a refused Blocking delivery is an error, never (nil, nil)
// — a nil answer would read as approval for a payment authorize.
func TestEventDataClass_BlockingRefusalIsAnError(t *testing.T) {
	const ev = "payment.evtest.authorize"
	_, d := evRig(t, evAgentID, allSaleClasses, nil, []string{ev})
	resp, err := SharedBus(d).PublishAuthorize(context.Background(), ev, map[string]any{"amount_cents": 100})
	if err == nil {
		t.Fatalf("refused blocking delivery returned (%s, nil), want an error", resp)
	}
	if !errors.Is(err, ErrEventDataClassDenied) {
		t.Fatalf("err = %v, want it to wrap ErrEventDataClassDenied", err)
	}
	if guestRan(t, d, evAgentID) {
		t.Fatal("refused blocking event reached the guest")
	}
}

// An Ask the plugin is refused behaves like a refused events:receive: the
// plugin is skipped (the next subscriber may answer), not an Ask error.
func TestEventDataClass_AskRefusalSkipsThePlugin(t *testing.T) {
	const ev = "tax.rate.ask"
	_, d := evRig(t, evAgentID, allSaleClasses, nil, []string{ev})
	resp, ok, err := SharedBus(d).Ask(context.Background(), ev, map[string]any{})
	if err != nil || ok || len(resp) != 0 {
		t.Fatalf("Ask = (%s, %v, %v), want no answer and no error", resp, ok, err)
	}
	if guestRan(t, d, evAgentID) {
		t.Fatal("refused ask reached the guest")
	}
	if len(denialRows(t, d, evAgentID, AIAgentPermission)) != 1 {
		t.Fatal("refused ask not audited")
	}
}

// A grant change is seen at the next delivery (checked live).
func TestEventDataClass_GrantCheckedLive(t *testing.T) {
	w, d := evRig(t, evAgentID, []string{"ai.agent", "ai.send.stock"}, nil, nil)
	if err := deliverEv(t, w, evAgentID, Event{Type: "stock.adjusted"}); err != nil || !guestRan(t, d, evAgentID) {
		t.Fatalf("granted stock.adjusted: err=%v", err)
	}
	if err := RevokePermission(context.Background(), d, evAgentID, "ai.send.stock"); err != nil {
		t.Fatal(err)
	}
	if err := deliverEv(t, w, evAgentID, Event{Type: "stock.adjusted"}); !errors.Is(err, ErrEventDataClassDenied) || guestRan(t, d, evAgentID) {
		t.Fatalf("revoked stock.adjusted: err=%v", err)
	}
}

// An installed manifest Sync cannot read leaves core unable to tell whether
// the plugin is an AI agent: it is marked broken (ut-docs#2280/#2304
// precedent) and refused every event.
func TestEventDataClass_UnreadableManifestFailsClosed(t *testing.T) {
	raw := evGuestBytes(t)
	const id = "com.test.evbadmanifest"
	withInstalledManifest(t, id, `{not json`)
	if err := writeFileWithParents(paths.Plugins(id, "1.0.0", "plugin.wasm"), raw); err != nil {
		t.Fatal(err)
	}
	d := streamDB(t, id, "events:receive")
	ctx := context.Background()
	w := NewWasmRuntime(paths.Plugins())
	w.Sync(ctx, d)
	t.Cleanup(func() { w.Close(context.Background()); SharedBus(d).ResetSubscribers() })
	info, ok, err := data.NewPluginRepo(d).GetPlugin(ctx, id, "")
	if err != nil || !ok {
		t.Fatalf("get plugin: ok=%v err=%v", ok, err)
	}
	if info.InstallState != data.PluginStateBroken {
		t.Fatalf("install_state = %q, want broken", info.InstallState)
	}
	if err := deliverEv(t, w, id, Event{Type: "ui.view.ask"}); err == nil || guestRan(t, d, id) {
		t.Fatalf("plugin with an unreadable manifest ran an event (err=%v)", err)
	}
}

// Every class in the registry is a real data class, and the ADR's entries
// are present exactly.
func TestEventDataClassRegistry(t *testing.T) {
	want := map[string][]string{
		"sale.completed":             {data.DataClassSales, data.DataClassCustomers, data.DataClassStaff},
		"stock.adjusted":             {data.DataClassStock},
		"customer.erased":            {data.DataClassCustomers},
		"catalog.identify":           {data.DataClassCatalog},
		"catalog.identify.confirmed": {data.DataClassCatalog},
		"ui.view.ask":                {},
		"ui.action.ask":              {},
		"plugin.button.pressed":      {},
		"agent.action.result":        {},
	}
	for ev, classes := range want {
		got, ok := EventDataClasses(ev)
		if !ok || !slices.Equal(got, classes) {
			t.Errorf("EventDataClasses(%q) = %v, %v; want %v, true", ev, got, ok, classes)
		}
	}
	for ev, classes := range eventDataClasses {
		if _, ok := want[ev]; !ok {
			t.Errorf("registry entry %q not pinned by this test", ev)
		}
		for _, c := range classes {
			if c != data.DataClassShop && !slices.Contains(data.AIDataClasses, c) {
				t.Errorf("%s: class %q is not a data class", ev, c)
			}
		}
	}
	if _, ok := EventDataClasses("tax.rate.ask"); ok {
		t.Error("tax.rate.ask must be unregistered")
	}
}
