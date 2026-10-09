package pages

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// The pick's targeted event (ADR-0121 amendment 2026-10-09 R2b,
// ut-docs#4007): after the learning step, core sends
// catalog.identify.confirmed {job_id, item_id, sku, stored} to the plugin
// that owned the slot only — never a broadcast — once it holds
// events:receive and view:inventory.

// confirmedRecorder records the catalog.identify.confirmed payloads one
// plugin receives.
type confirmedRecorder struct {
	mu   sync.Mutex
	got  []map[string]any
	refs []int // ai_ref files of itm-tea on disk at each delivery
}

func (c *confirmedRecorder) all() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.got...)
}

// addConfirmedHook declares pluginID's catalog.identify.confirmed hook
// (manifest "hooks"), as a plugin that wants the event must.
func addConfirmedHook(t *testing.T, h *identifyHarness, pluginID string) {
	t.Helper()
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES(?,?,?,'confirmed',1)`, pluginID+"-hconfirmed", pluginID, identifyConfirmedEvent); err != nil {
		t.Fatal(err)
	}
}

// subscribeConfirmed hooks pluginID to catalog.identify.confirmed and
// grants it the given permissions.
func subscribeConfirmed(t *testing.T, h *identifyHarness, pluginID string, perms ...string) *confirmedRecorder {
	t.Helper()
	addConfirmedHook(t, h, pluginID)
	for _, p := range perms {
		if _, err := h.d.Db.Exec(`INSERT OR IGNORE INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, pluginID+"-"+p, pluginID, p); err != nil {
			t.Fatal(err)
		}
	}
	rec := &confirmedRecorder{}
	if _, err := plugins.SharedBus(h.d.Db).SubscribeWithHandler(t.Context(), pluginID, []string{identifyConfirmedEvent},
		func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			rec.mu.Lock()
			rec.got = append(rec.got, p)
			rec.refs = append(rec.refs, len(aiRefs(t, "itm-tea")))
			rec.mu.Unlock()
			return nil, nil
		}); err != nil {
		t.Fatalf("subscribe %s: %v", identifyConfirmedEvent, err)
	}
	return rec
}

func TestPluginIdentify_ConfirmedGoesToSlotOwnerOnly_4007(t *testing.T) {
	h := newPickHarness(t)
	owner := subscribeConfirmed(t, h, viewPluginID, "view:inventory")
	// A second plugin with every permission still never receives it.
	other := subscribeConfirmed(t, h, "com.test.other", "events:receive", "view:inventory")

	job, _ := h.identifyWith(realJPEG(t))
	if rec := h.pick(job, "TEA-1", "1"); rec.Code != 200 {
		t.Fatalf("pick = %d", rec.Code)
	}
	waitPick(t, "the confirmed event", func() bool { return len(owner.all()) == 1 })
	got := owner.all()[0]
	want := map[string]any{"job_id": job, "item_id": "itm-tea", "sku": "TEA-1", "stored": true}
	if len(got) != len(want) {
		t.Errorf("payload = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, got[k], v)
		}
	}
	owner.mu.Lock()
	refs := owner.refs[0]
	owner.mu.Unlock()
	if refs != 1 {
		t.Errorf("ai_ref files at delivery = %d, want 1: stored:true means item_image_open finds it", refs)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(other.all()); n != 0 {
		t.Fatalf("a second subscriber received %d confirmed events, want 0 (targeted, never broadcast)", n)
	}
	// Taken once: a second pick of the same job sends nothing more.
	h.pick(job, "TEA-1", "1")
	time.Sleep(50 * time.Millisecond)
	if n := len(owner.all()); n != 1 {
		t.Fatalf("confirmed events after a second pick = %d, want 1", n)
	}
}

func TestPluginIdentify_ConfirmedNeedsViewInventory_4007(t *testing.T) {
	h := newPickHarness(t)
	owner := subscribeConfirmed(t, h, viewPluginID) // events:receive only (the harness's)

	job, _ := h.identifyWith(realJPEG(t))
	h.pick(job, "TEA-1", "1")
	waitPick(t, "the ai_ref on disk", func() bool { return len(aiRefs(t, "itm-tea")) == 1 })
	waitPick(t, "the denial audit row", func() bool {
		var n int
		_ = h.d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='permission_denied' AND data_json LIKE '%view:inventory%'`).Scan(&n)
		return n == 1
	})
	if n := len(owner.all()); n != 0 {
		t.Fatalf("a plugin without view:inventory received %d confirmed events", n)
	}
	// A second pick audits no second denial (CheckPermissionAuditOnce).
	job, _ = h.identifyWith(realJPEG(t))
	h.pick(job, "TEA-1", "1")
	h.d.WaitForAsyncWork()
	var n int
	_ = h.d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='permission_denied' AND data_json LIKE '%view:inventory%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("view:inventory denial rows after two picks = %d, want 1", n)
	}
}

// An identify plugin that never hooked the confirmed event (#3873's
// contract) is skipped silently: no permission_denied row per pick.
func TestPluginIdentify_ConfirmedUnhookedPluginNoAuditNoise_4007(t *testing.T) {
	h := newPickHarness(t)
	job, _ := h.identifyWith(realJPEG(t))
	h.pick(job, "TEA-1", "1")
	waitPick(t, "the ai_ref on disk", func() bool { return len(aiRefs(t, "itm-tea")) == 1 })
	h.d.WaitForAsyncWork()
	var n int
	_ = h.d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='permission_denied'`).Scan(&n)
	if n != 0 {
		t.Fatalf("permission_denied rows = %d for a plugin that never hooked %s, want 0", n, identifyConfirmedEvent)
	}
}

// sku is the item's own SKU, not the picked code: a barcode pick resolves
// to a line whose SKU is the barcode (as the production resolver does).
func TestPluginIdentify_ConfirmedSendsItemSKUNotPickedCode_4007(t *testing.T) {
	h := newPickHarness(t)
	h.d.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, stubResolver{
		"TEA-1":         {SKU: "TEA-1", Name: "Tea", Qty: 1, PriceCents: 250, ItemID: "itm-tea", TaxRateBP: 2000},
		"5012345678900": {SKU: "5012345678900", Name: "Tea", Qty: 1, PriceCents: 250, ItemID: "itm-tea", TaxRateBP: 2000},
	})
	owner := subscribeConfirmed(t, h, viewPluginID, "view:inventory")
	job, _ := h.identifyWith(realJPEG(t))
	h.pick(job, "5012345678900", "1")
	waitPick(t, "the confirmed event", func() bool { return len(owner.all()) == 1 })
	if got := owner.all()[0]["sku"]; got != "TEA-1" {
		t.Fatalf("payload sku = %v, want the item's SKU TEA-1", got)
	}
}

func TestPluginIdentify_ConfirmedStoredFalse_4007(t *testing.T) {
	h := newPickHarness(t)
	owner := subscribeConfirmed(t, h, viewPluginID, "view:inventory")

	job, _ := h.identifyWith(jpegBytes(2048)) // a JPEG header, no image
	h.pick(job, "TEA-1", "1")
	waitPick(t, "the confirmed event", func() bool { return len(owner.all()) == 1 })
	if got := owner.all()[0]; got["stored"] != false || got["item_id"] != "itm-tea" {
		t.Fatalf("payload = %v, want stored:false for itm-tea", got)
	}
	if n := len(aiRefs(t, "itm-tea")); n != 0 {
		t.Fatalf("ai_ref files = %d, want 0", n)
	}
	// A WebP photo is never stored either.
	job, _ = h.identifyWith(append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 64)...))
	h.pick(job, "TEA-1", "1")
	waitPick(t, "the second confirmed event", func() bool { return len(owner.all()) == 2 })
	if got := owner.all()[1]; got["stored"] != false {
		t.Fatalf("WebP payload = %v, want stored:false", got)
	}
}

func TestPluginIdentify_ConfirmedNotSentWithoutSlotOrItem_4007(t *testing.T) {
	h := newPickHarness(t)
	owner := subscribeConfirmed(t, h, viewPluginID, "view:inventory")

	// No slot for this job: nothing to confirm.
	h.pick(strings.Repeat("ab", 16), "TEA-1", "1")
	// A slot, but the sku names no item: no item_id to send.
	job, _ := h.identifyWith(realJPEG(t))
	h.pick(job, "NOPE-9", "1")
	h.d.WaitForAsyncWork()
	if n := len(owner.all()); n != 0 {
		t.Fatalf("confirmed events = %d, want 0", n)
	}
	assertNoStaged(t)
}

func TestPluginIdentify_ConfirmedBrokenPluginDelaysNothing_4007(t *testing.T) {
	h := newPickHarness(t)
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('vi',?,'view:inventory',1)`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	addConfirmedHook(t, h, viewPluginID)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	plugins.SharedBus(h.d.Db).ResetSubscribers()
	if _, err := plugins.SharedBus(h.d.Db).SubscribeWithHandler(t.Context(), viewPluginID, []string{identifyEvent},
		func(context.Context, plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"document":{"version":1,"components":[{"type":"suggestions","items":[
				{"label":{"literal":"Tea"},"effect":{"add_to_basket":{"sku":"TEA-1","qty":1}}}]}]}}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SharedBus(h.d.Db).SubscribeWithHandler(t.Context(), viewPluginID, []string{identifyConfirmedEvent},
		func(ctx context.Context, _ plugins.Event) (json.RawMessage, error) {
			select { // a plugin that hangs until its deadline
			case <-release:
			case <-ctx.Done():
			}
			return nil, os.ErrDeadlineExceeded
		}); err != nil {
		t.Fatal(err)
	}
	job, _ := h.identifyWith(realJPEG(t))
	start := time.Now()
	if rec := h.pick(job, "TEA-1", "1"); rec.Code != 200 {
		t.Fatalf("pick = %d", rec.Code)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("pick took %v with a hanging confirmed handler", d)
	}
	waitPick(t, "the ai_ref on disk", func() bool { return len(aiRefs(t, "itm-tea")) == 1 })
}
