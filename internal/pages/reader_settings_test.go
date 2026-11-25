package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
)

// ut-docs#6538 phase 2 (G9, G13a): the Tills page shows this main till's id (so a
// phone can be paired without mDNS) and carries the manager's switch for phone
// card readers, which until now had no UI at all.

func managerReq(method, path string, form url.Values) *http.Request {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	return auth.WithUser(req, auth.User{ID: "m1", Role: "manager"})
}

func TestTillsPageShowsMainTillIdToManagerOnly(t *testing.T) {
	g := newReaderRig(t, false)
	id, err := discovery.TillID(context.Background(), data.NewSettingsRepo(g.d.Db))
	if err != nil || id == "" {
		t.Fatalf("till id: %q %v", id, err)
	}
	rec := g.serve(managerReq(http.MethodGet, "/ui/tills/reader-settings", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("manager = %d, want 200 with the till id %q in:\n%s", rec.Code, id, rec.Body)
	}
	cashier := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/tills/reader-settings", nil), auth.User{ID: "c1", Role: "cashier"})
	if rec := g.serve(cashier); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), id) {
		t.Fatalf("cashier = %d body has the id=%v, want 403 and no id", rec.Code, strings.Contains(rec.Body.String(), id))
	}
	if rec := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/reader-settings", nil)); rec.Code != http.StatusForbidden {
		t.Fatalf("no session = %d, want 403", rec.Code)
	}
	// A following (additional) till is not a main: nothing to show.
	if err := g.d.Settings.Set(context.Background(), "sync.primary_url", "http://main.example"); err != nil {
		t.Fatal(err)
	}
	if rec := g.serve(managerReq(http.MethodGet, "/ui/tills/reader-settings", nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("additional till = %d, want 204", rec.Code)
	}
}

func TestPhonePairingSwitchNeedsManagerPinAndWritesTillLocalSetting(t *testing.T) {
	g := newReaderRig(t, true) // UT_AUTH=off: the PIN gate is bypassed for the write test
	ctx := context.Background()
	if err := g.d.Settings.Set(ctx, settingPhoneReaderPairing, ""); err != nil {
		t.Fatal(err)
	}
	if phoneReaderPairingOn(ctx, g.d) {
		t.Fatal("the switch must start off")
	}
	mgr := seedDataAPIManager(t, g.d.Db) // the audit row's actor must be a real user
	asMgr := func(path string, form url.Values) *http.Request {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return auth.WithUser(req, auth.User{ID: mgr, Role: "manager"})
	}
	rec := g.serve(asMgr("/ui/tills/reader-settings/phone-pairing", url.Values{"enabled": {"on"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch on = %d %s", rec.Code, rec.Body)
	}
	if v, _, _ := g.d.Settings.Get(ctx, settingPhoneReaderPairing); v != "on" || !phoneReaderPairingOn(ctx, g.d) {
		t.Fatalf("setting = %q, want on", v)
	}
	if n := countRows(t, g.d, `SELECT COUNT(*) FROM audit_log WHERE action = 'card_readers_phone_pairing_changed'`); n != 1 {
		t.Fatalf("%d audit rows for the switch, want 1", n)
	}
	rec = g.serve(managerReq(http.MethodPost, "/ui/tills/reader-settings/phone-pairing", url.Values{"enabled": {"off"}}))
	if v, _, _ := g.d.Settings.Get(ctx, settingPhoneReaderPairing); rec.Code != http.StatusOK || v != "off" {
		t.Fatalf("switch off = %d setting %q", rec.Code, v)
	}
	if rec := g.serve(managerReq(http.MethodPost, "/ui/tills/reader-settings/phone-pairing", url.Values{"enabled": {"maybe"}})); rec.Code != http.StatusBadRequest {
		t.Fatalf("a junk value = %d, want 400", rec.Code)
	}

	// With auth on: no PIN -> 403 and the setting does not move; a cashier too.
	t.Setenv("UT_AUTH", "")
	rec = g.serve(managerReq(http.MethodPost, "/ui/tills/reader-settings/phone-pairing", url.Values{"enabled": {"on"}, "manager_pin": {"0000"}}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong PIN = %d, want 403", rec.Code)
	}
	cashier := auth.WithUser(httptest.NewRequest(http.MethodPost, "/ui/tills/reader-settings/phone-pairing", strings.NewReader("enabled=on")), auth.User{ID: "c1", Role: "cashier"})
	cashier.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := g.serve(cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	if phoneReaderPairingOn(ctx, g.d) {
		t.Fatal("a refused switch turned phone readers on")
	}
}

func TestPhonePairingSwitchNeverSyncs(t *testing.T) {
	// The key is the till's own: an admin dump never carries it and a pull
	// never overwrites it (data.PerTillSettingPrefixes; checked in the data
	// package). Here: the route writes exactly that key and nothing else.
	g := newReaderRig(t, true)
	ctx := context.Background()
	before := countRows(t, g.d, `SELECT COUNT(*) FROM settings`)
	_ = g.serve(managerReq(http.MethodPost, "/ui/tills/reader-settings/phone-pairing", url.Values{"enabled": {"off"}}))
	if v, ok, _ := g.d.Settings.Get(ctx, "card_readers.phone_pairing"); !ok || v != "off" {
		t.Fatalf("setting = %q ok=%v", v, ok)
	}
	if after := countRows(t, g.d, `SELECT COUNT(*) FROM settings`); after > before+1 {
		t.Fatalf("the switch wrote %d settings rows", after-before)
	}
}

func TestPhonePairingSwitchSaysItIsATestFeature(t *testing.T) {
	g := newReaderRig(t, true)
	body := g.serve(managerReq(http.MethodGet, "/ui/tills/reader-settings", nil)).Body.String()
	for _, want := range []string{`name="manager_pin"`, `hx-post="/ui/tills/reader-settings/phone-pairing"`, "Test feature"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment lacks %q:\n%s", want, body)
		}
	}
}
