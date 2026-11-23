package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/lanaccess"
)

// ut-docs#6955: the gate in front of a desktop main till.
func TestLANPolicy_AutoOnOffAndWindow(t *testing.T) {
	mux, d, _ := newPairingAPITestDeps(t)
	t.Cleanup(func() { lanaccess.SetPolicy(nil); lanaccess.CloseWindow() })
	lanaccess.CloseWindow() // other tests show codes in the same process
	_ = mux
	ctx := t.Context()

	// Fresh shop, nothing shown: closed (nobody but this PC).
	if lanaccess.Allowed() {
		t.Fatal("fresh main till must not admit other devices")
	}
	// Always / never override.
	for mode, want := range map[string]bool{lanModeOn: true, lanModeOff: false} {
		if err := d.Settings.Set(ctx, keyLANAccess, mode); err != nil {
			t.Fatal(err)
		}
		time.Sleep(lanPolicyTTL + 50*time.Millisecond)
		if got := lanaccess.Allowed(); got != want {
			t.Fatalf("mode %s: allowed=%v want %v", mode, got, want)
		}
	}
	// Garbage value reads as auto.
	_ = d.Settings.Set(ctx, keyLANAccess, "bogus")
	if lanMode(ctx, d) != lanModeAuto {
		t.Fatal("unknown mode must read as auto")
	}
	// Auto: a shown pairing code opens the gate.
	time.Sleep(lanPolicyTTL + 50*time.Millisecond)
	if lanaccess.Allowed() {
		t.Fatal("auto, no code, no other till: closed")
	}
	lanaccess.OpenWindow(time.Minute)
	if !lanaccess.Allowed() {
		t.Fatal("auto + pairing code shown: open")
	}
	// Auto: a shop with another till stays open.
	if _, err := data.NewTillsRepo(d.Db).InsertTill(ctx, "Till 2", "hash"); err != nil {
		t.Fatal(err)
	}
	if !lanDecide(ctx, d, data.NewTillsRepo(d.Db)) {
		t.Fatal("auto + a second till: open")
	}
}

func TestLANAccessSettingEndpoints(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, d, _ := newPairingAPITestDeps(t)
	t.Cleanup(func() { lanaccess.SetPolicy(nil) })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/settings/lan-access", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `data-testid="lan-access-auto"`) {
		t.Fatalf("fragment: %d %s", rec.Code, rec.Body.String())
	}
	post := func(mode string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/lan-access", strings.NewReader(url.Values{"mode": {mode}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, req)
		return r.Code
	}
	if c := post("sideways"); c != 400 {
		t.Fatalf("bad mode: %d", c)
	}
	if c := post("on"); c != 200 {
		t.Fatalf("save: %d", c)
	}
	if lanMode(t.Context(), d) != lanModeOn {
		t.Fatal("mode not stored")
	}
}

// A cashier is sale-only: the network-access control is manager territory.
func TestLANAccessSettingEndpoints_CashierRefused(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, d, _ := newPairingAPITestDeps(t)
	t.Cleanup(func() { lanaccess.SetPolicy(nil) })

	// No signed-in user at all: refused too.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/settings/lan-access", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET unauthenticated: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/settings/lan-access", strings.NewReader("mode=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST unauthenticated: %d", rec.Code)
	}
	if lanMode(t.Context(), d) == lanModeOn {
		t.Fatal("an unauthenticated caller changed the mode")
	}
}
