package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6458: Settings > Payments > Tap to Pay on iPhone.

// fakeTapBridge is a devicepay.Bridge whose readiness and Setup answer a test
// controls. Setup blocks until gate is closed when gate is non-nil.
type fakeTapBridge struct {
	mu         sync.Mutex
	caps       string
	setupCalls int
	eduCalls   int
	gate       chan struct{}
	setupReply string
}

func (b *fakeTapBridge) Capabilities() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.caps
}
func (b *fakeTapBridge) Collect(string) string { return `{"status":"pending"}` }
func (b *fakeTapBridge) Cancel(string)         {}
func (b *fakeTapBridge) Setup(string) string {
	b.mu.Lock()
	b.setupCalls++
	gate, reply := b.gate, b.setupReply
	b.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return reply
}
func (b *fakeTapBridge) ShowEducation(string) string {
	b.mu.Lock()
	b.eduCalls++
	b.mu.Unlock()
	return `{"ok":true}`
}
func (b *fakeTapBridge) setCaps(s string) { b.mu.Lock(); b.caps = s; b.mu.Unlock() }
func (b *fakeTapBridge) calls() int       { b.mu.Lock(); defer b.mu.Unlock(); return b.setupCalls }

const capsNeedsTerms = `{"tap_to_pay":{"stripe":{"ready":false,"state":"needs_setup","reason":"terms","progress":0}}}`
const capsReady = `{"tap_to_pay":{"stripe":{"ready":true,"state":"ready","progress":0}}}`

type tapSettingsEnv struct {
	mux    http.Handler
	dp     *common.Deps
	bridge *fakeTapBridge
	broker *fakeBroker
	plugin string
	users  map[string]auth.User
}

func tapSettingsRig(t *testing.T) *tapSettingsEnv {
	t.Helper()
	mux, dp := authRig(t)
	b := &fakeTapBridge{caps: capsNeedsTerms, setupReply: `{"ok":true}`}
	devicepay.SetBridge(b)
	t.Cleanup(func() { devicepay.SetBridge(nil) })
	e := &tapSettingsEnv{mux: mux, dp: dp, bridge: b, broker: newFakeBroker(t)}
	e.broker.setStatus(`{"ready":true,"source":"own","livemode":false}`)
	e.users = map[string]auth.User{}
	for _, role := range []string{"manager", "cashier"} {
		id, err := data.NewAuthRepo(dp.Db).CreateUser(context.Background(), "tap-"+role, strings.ToUpper(role[:1])+role[1:], role)
		if err != nil {
			t.Fatal(err)
		}
		e.users[role] = auth.User{ID: id, Role: role}
	}
	e.plugin = "com.universaltill.payment-stripe"
	seedReaderPlugin(t, dp, e.plugin, "stripe_tap", false)
	if _, err := dp.Db.Exec(`UPDATE plugin_entries SET config_json = '{"tap":true,"provider":"stripe"}' WHERE plugin_id = ?`, e.plugin); err != nil {
		t.Fatal(err)
	}
	for i, perm := range []string{plugins.TapDevicePermission, plugins.BrokerNetPermission} {
		if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, ?, 0)`, "tp"+string(rune('a'+i)), e.plugin, perm); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *tapSettingsEnv) do(role, method, path string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.users[role]))
	return rec
}

func (e *tapSettingsEnv) granted(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	tap := map[string]bool{plugins.TapDevicePermission: true, plugins.BrokerNetPermission: true, "storage": true}
	perms, err := data.NewPluginRepo(e.dp.Db).ListPermissions(context.Background(), e.plugin)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range perms {
		if tap[p.Permission] { // events:receive is the fixture's own grant
			out[p.Permission] = p.Granted
		}
	}
	return out
}

func want(t *testing.T, rec *httptest.ResponseRecorder, code int, subs ...string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d; body %.300s", rec.Code, code, rec.Body.String())
	}
	for _, s := range subs {
		if !strings.Contains(rec.Body.String(), s) {
			t.Fatalf("body lacks %q: %s", s, rec.Body.String())
		}
	}
}

func TestTapSettings_CashierForbidden(t *testing.T) {
	e := tapSettingsRig(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/settings/tap-to-pay"}, {"GET", "/settings/tap-to-pay/status"},
		{"POST", "/settings/tap-to-pay/setup"}, {"POST", "/settings/tap-to-pay/education"}, {"POST", "/settings/tap-to-pay/grant"},
	} {
		if rec := e.do("cashier", c.method, c.path, url.Values{}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as cashier = %d, want 403", c.method, c.path, rec.Code)
		}
	}
	if e.bridge.calls() != 0 || e.bridge.eduCalls != 0 {
		t.Fatalf("native calls by a cashier: setup=%d education=%d, want 0", e.bridge.calls(), e.bridge.eduCalls)
	}
	for perm, g := range e.granted(t) {
		if g {
			t.Fatalf("%s granted by a cashier", perm)
		}
	}
}

func TestTapSettings_NoBridge(t *testing.T) {
	e := tapSettingsRig(t)
	devicepay.SetBridge(nil)
	if tapSettingsAvailable() {
		t.Fatal("tapSettingsAvailable() = true with no bridge, want false (nav link hidden)")
	}
	rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
	want(t, rec, 200, `data-testid="tap-unavailable"`, "isn&#39;t available on this device")
	if strings.Contains(rec.Body.String(), `data-testid="tap-setup"`) {
		t.Fatal("Set up button rendered with no bridge")
	}
	if rec := e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("setup with no bridge = %d, want 404", rec.Code)
	}
	if e.bridge.calls() != 0 {
		t.Fatalf("Setup called %d times with no bridge", e.bridge.calls())
	}
}

func TestTapSettings_NeedsSetupShowsOneAction(t *testing.T) {
	e := tapSettingsRig(t)
	rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
	want(t, rec, 200, `data-state="needs_setup"`, `data-testid="tap-setup"`, "Using your own Stripe account.", `data-testid="tap-test-mode"`)
	if strings.Contains(rec.Body.String(), `data-testid="tap-grant"`) {
		t.Fatal("Allow card taps offered before the first unfinished step (Set up) is done")
	}
	if strings.Contains(rec.Body.String(), "device:tap_to_pay") {
		t.Fatal("a permission key name is shown")
	}
}

func TestTapSettings_Setup_SuccessAndDoubleClick(t *testing.T) {
	e := tapSettingsRig(t)
	e.bridge.gate = make(chan struct{})
	rec := e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{})
	want(t, rec, 200, `data-state="preparing"`, "Follow the steps on the iPhone screen", "hx-trigger=\"every 2s\"")
	rec = e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{})
	want(t, rec, 200, `data-testid="tap-already"`, "Already running.")
	e.bridge.setCaps(capsReady)
	close(e.bridge.gate)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rows := countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_to_pay.setup'`); rows == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := e.bridge.calls(); n != 1 {
		t.Fatalf("native Setup calls = %d, want exactly 1 for a double click", n)
	}
	if rows := countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_to_pay.setup' AND data_json LIKE '%"ok":true%'`); rows != 1 {
		t.Fatalf("audit rows tap_to_pay.setup ok=true: %d, want 1", rows)
	}
	rec = e.do("manager", "GET", "/settings/tap-to-pay/status", nil)
	want(t, rec, 200, "Set up finished.")
	if strings.Contains(rec.Body.String(), `hx-trigger="every 2s"`) {
		t.Fatal("the status poll keeps running after Setup finished")
	}
}

func TestTapSettings_Setup_TermsNotAccepted(t *testing.T) {
	e := tapSettingsRig(t)
	e.bridge.setupReply = `{"ok":false,"reason":"terms"}`
	e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{})
	var rec *httptest.ResponseRecorder
	for i := 0; i < 300; i++ {
		rec = e.do("manager", "GET", "/settings/tap-to-pay/status", nil)
		if strings.Contains(rec.Body.String(), "tap-setup-failed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	want(t, rec, 200, "Set up didn&#39;t finish. Nothing was charged.", "Apple&#39;s terms haven&#39;t been accepted yet.")
	if !strings.Contains(rec.Body.String(), `data-testid="tap-setup"`) {
		t.Fatal("no Try again (Set up) button after a failed Setup: dead end")
	}
}

func TestTapSettings_Setup_Timeout(t *testing.T) {
	e := tapSettingsRig(t)
	old := tapSetupTimeout
	tapSetupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { tapSetupTimeout = old })
	e.bridge.gate = make(chan struct{})
	t.Cleanup(func() { close(e.bridge.gate) }) // lets the native goroutine end
	e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{})
	var rec *httptest.ResponseRecorder
	for i := 0; i < 300; i++ {
		rec = e.do("manager", "GET", "/settings/tap-to-pay/status", nil)
		if strings.Contains(rec.Body.String(), "tap-setup-failed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	want(t, rec, 200, "Apple didn&#39;t answer in time. Try again.")
}

func TestTapSettings_GrantPermissions(t *testing.T) {
	e := tapSettingsRig(t)
	e.bridge.setCaps(capsReady)
	for perm, g := range e.granted(t) {
		if g {
			t.Fatalf("%s starts granted", perm)
		}
	}
	rec := e.do("manager", "POST", "/settings/tap-to-pay/grant", url.Values{"permission": {"storage"}})
	want(t, rec, 200, `data-testid="tap-permission-granted"`, `data-state="ready"`)
	got := e.granted(t)
	if !got[plugins.TapDevicePermission] || !got[plugins.BrokerNetPermission] {
		t.Fatalf("grants = %v, want both Tap to Pay permissions granted", got)
	}
	if _, ok := got["storage"]; ok {
		t.Fatalf("a body-supplied permission name reached the plugin: %v", got)
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_to_pay.permissions_granted'`); n != 1 {
		t.Fatalf("grant audit rows = %d, want 1", n)
	}
}

// Review R1: a plugin that only names provider "stripe" in its tap entry is
// not the Stripe plugin and never receives the grant.
func TestTapSettings_GrantRefusesForeignPlugin(t *testing.T) {
	e := tapSettingsRig(t)
	if _, err := e.dp.Db.Exec(`UPDATE plugin_entries SET config_json = '{}' WHERE plugin_id = ?`, e.plugin); err != nil {
		t.Fatal(err)
	}
	seedReaderPlugin(t, e.dp, "com.example.lookalike", "look_tap", false)
	if _, err := e.dp.Db.Exec(`UPDATE plugin_entries SET config_json = '{"tap":true,"provider":"stripe"}' WHERE plugin_id = 'com.example.lookalike'`); err != nil {
		t.Fatal(err)
	}
	rec := e.do("manager", "POST", "/settings/tap-to-pay/grant", url.Values{})
	want(t, rec, 409)
	for perm, g := range e.granted(t) {
		if g {
			t.Fatalf("%s granted to a plugin that is not the Stripe plugin", perm)
		}
	}
}

func TestTapSettings_CloudState(t *testing.T) {
	cases := []struct {
		name, answer string
		wantText     string
	}{
		{"own ready", `{"ready":true,"source":"own","livemode":false}`, "Using your own Stripe account."},
		{"connect ready", `{"ready":true,"source":"connect","livemode":true}`, "Using the Stripe account connected to this shop."},
		{"not configured", `{"ready":false,"source":"own","reason":"not_configured"}`, "Add your Stripe key in the shop&#39;s online account (my.universaltill.com)"},
		{"needs attention", `{"ready":false,"source":"own","reason":"needs_attention"}`, "Your Stripe key needs attention."},
		{"address", `{"ready":false,"source":"own","reason":"address_incomplete"}`, "Add the shop&#39;s full address"},
		{"cloud down", ``, "Can&#39;t reach the payment service right now."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := tapSettingsRig(t)
			e.broker.setStatus(c.answer)
			rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
			want(t, rec, 200, c.wantText)
			if c.name == "cloud down" && strings.Contains(rec.Body.String(), "isn&#39;t set up") {
				t.Fatal("an unreachable cloud was worded as not set up")
			}
			if c.name == "connect ready" && strings.Contains(rec.Body.String(), `data-testid="tap-test-mode"`) {
				t.Fatal("test-mode tag shown for a live answer")
			}
		})
	}
}

func (e *tapSettingsEnv) joinAs(t *testing.T, role string) {
	t.Helper()
	ctx := context.Background()
	if err := e.dp.Settings.Set(ctx, "sync.primary_url", "http://main.example"); err != nil {
		t.Fatal(err)
	}
	if role != "" {
		if err := e.dp.Settings.Set(ctx, tillRoleSettingsKey, role); err != nil {
			t.Fatal(err)
		}
	}
}

// A satellite (and a joined till with no recorded role) is not offered Tap to
// Pay: the panel says so with the reworded reason (ADR-0198 Amendment 2026-11-23).
func TestTapSettings_SatelliteTillUnavailable(t *testing.T) {
	for _, role := range []string{data.TillRoleSatellite, ""} {
		e := tapSettingsRig(t)
		e.joinAs(t, role)
		rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
		want(t, rec, 200, "This till can&#39;t take Tap to Pay. Only the main till and additional tills can.")
		if strings.Contains(rec.Body.String(), `data-testid="tap-setup"`) || strings.Contains(rec.Body.String(), `data-testid="tap-additional-note"`) {
			t.Fatalf("role %q: Set up button or additional note on a till that cannot take tap", role)
		}
		if rec := e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{}); rec.Code != http.StatusConflict {
			t.Fatalf("role %q: setup = %d, want 409", role, rec.Code)
		}
		if e.bridge.calls() != 0 {
			t.Fatalf("role %q: native Setup called %d times", role, e.bridge.calls())
		}
	}
}

// An explicit additional till is like a main for the panel (setup allowed, same
// steps) plus the additional note, shown as visible text.
func TestTapSettings_AdditionalTill(t *testing.T) {
	e := tapSettingsRig(t)
	e.joinAs(t, data.TillRoleAdditional)
	rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
	want(t, rec, 200, `data-testid="tap-additional-note"`, enText(t, "tap.settings.note.additional"), `data-testid="tap-setup"`)
	if strings.Contains(rec.Body.String(), `data-state="unavailable"`) || strings.Contains(rec.Body.String(), "Only the main till") {
		t.Fatalf("an additional till is not unavailable: %s", rec.Body.String())
	}
	if rec := e.do("manager", "POST", "/settings/tap-to-pay/setup", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("setup on an additional till = %d, want 200", rec.Code)
	}
	// Allow: manager only, audited, enables the permissions locally like a main.
	if rec := e.do("cashier", "POST", "/settings/tap-to-pay/grant", url.Values{}); rec.Code == http.StatusOK {
		t.Fatal("a cashier granted the Tap to Pay permissions")
	}
	want(t, e.do("manager", "POST", "/settings/tap-to-pay/grant", url.Values{}), 200)
	for perm, g := range e.granted(t) {
		if !g {
			t.Fatalf("%s not granted on an additional till", perm)
		}
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_to_pay.permissions_granted'`); n != 1 {
		t.Fatalf("grant audit rows = %d, want 1", n)
	}
}

// The main till never shows the additional note.
func TestTapSettings_MainTillHasNoAdditionalNote(t *testing.T) {
	e := tapSettingsRig(t)
	rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
	if strings.Contains(rec.Body.String(), `data-testid="tap-additional-note"`) {
		t.Fatal("additional note on the main till")
	}
}

// UX copy section 4: an old additional till whose credential the cloud refuses
// as role-unrecorded says "set this till up again", never "can't reach".
func TestTapSettings_RoleUnrecordedSaysSetUpAgain(t *testing.T) {
	for _, code := range []string{"device_role_unrecorded", "device_role_unconfirmed"} {
		e := tapSettingsRig(t)
		e.joinAs(t, data.TillRoleAdditional)
		e.broker.setRoleRefused(true)
		e.broker.setRoleCode(code)
		rec := e.do("manager", "GET", "/settings/tap-to-pay", nil)
		want(t, rec, 200, enText(t, "tap.settings.source.setup_again"))
		if strings.Contains(rec.Body.String(), "Can&#39;t reach the payment service") || strings.Contains(rec.Body.String(), code) {
			t.Fatalf("%s: worded as unreachable or showed the code", code)
		}
	}
}

// A demo till has no bridge and no provider: the page and its three POSTs are
// not served (when in doubt, deny: demo_mode_test classifies every route).
func TestTapSettings_NotServedInDemoMode(t *testing.T) {
	for _, k := range []string{
		"GET /settings/tap-to-pay", "GET /settings/tap-to-pay/status",
		"POST /settings/tap-to-pay/setup", "POST /settings/tap-to-pay/grant", "POST /settings/tap-to-pay/education",
	} {
		if !demoDeniedRoutes[k] {
			t.Errorf("%s is not refused in demo mode", k)
		}
	}
}

// ADR-0198 2 conditions 2 and 4 (ut-docs#6458): a tap entry is offered only
// when this device is ready or a paired phone reader exists for it.
func TestTapOffering_Capability(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	pm := []data.PaymentMethod{{ID: "cash"}, {ID: "tapstripe"}}
	ids := func(ms []data.PaymentMethod) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.ID)
		}
		return strings.Join(s, ",")
	}
	b := &fakeTapBridge{caps: `{"tap_to_pay":{"stripe":{"ready":false,"state":"needs_setup","reason":"terms"}}}`}
	devicepay.SetBridge(b)
	t.Cleanup(func() { devicepay.SetBridge(nil) })

	if got := ids(withoutUnreadyTapEntries(ctx, e.dp, pm)); got != "cash" {
		t.Fatalf("not ready, no phone reader: grid = %q, want cash", got)
	}
	var tErr *tapStartFailedError
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); !asTapStart(err, &tErr) || !tErr.NotReady {
		t.Fatalf("not ready: gate = %v, want tapStartFailedError{NotReady}", err)
	}
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"cash"}); err != nil {
		t.Fatalf("a cash leg must pass: %v", err)
	}

	b.setCaps(capsReady)
	if got := ids(withoutUnreadyTapEntries(ctx, e.dp, pm)); got != "cash,tapstripe" {
		t.Fatalf("ready: grid = %q, want cash,tapstripe", got)
	}
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("ready: gate refused: %v", err)
	}

	// Not ready here, but a paired phone reader collects it (no regression, ADR-0198 6).
	b.setCaps(capsNeedsTerms)
	if _, err := e.dp.Db.Exec(`INSERT INTO card_readers (id, name, method_key, plugin_id, reader_ref, kind, source, sort_order, transport) VALUES ('r1','Phone','tapstripe','p.tap','ref1','phone','paired',1,'internet')`); err != nil {
		t.Fatalf("seed phone reader: %v", err)
	}
	if got := ids(withoutUnreadyTapEntries(ctx, e.dp, pm)); got != "cash,tapstripe" {
		t.Fatalf("phone reader: grid = %q, want cash,tapstripe", got)
	}
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("phone reader: gate refused: %v", err)
	}
}

func TestTapOffering_DeclinedAndPinPanels(t *testing.T) {
	cases := []struct {
		name, code string
		wantText   string
		notText    string
	}{
		{"declined", "card_declined", "Card declined. Nothing was charged.", "needs a PIN"},
		{"pin fallback", "offline_pin_required", "This card needs a PIN this iPhone can&#39;t take here. Nothing was charged.", "Card declined"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := tapRig(t)
			b := &fakeTapBridge{caps: capsReady}
			devicepay.SetBridge(b)
			t.Cleanup(func() { devicepay.SetBridge(nil) })
			e.broker.setStatus(piStatus("requires_payment_method", 0)) // the broker confirms nothing was charged
			e.plugin.answer = func(int) string { return "DECLINE:" + c.code }
			rec := e.tender(t)
			want(t, rec, 200, c.wantText)
			body := rec.Body.String()
			if strings.Contains(body, c.notText) {
				t.Fatalf("body shows %q: %s", c.notText, body)
			}
			if strings.Contains(body, c.code) {
				t.Fatalf("the bank/decline code %q reached the cashier", c.code)
			}
			if e.sales(t) != 0 || e.dp.Engine.TenderLock() != "" {
				t.Fatalf("sales=%d lock=%q, want 0 and none (basket unlocked)", e.sales(t), e.dp.Engine.TenderLock())
			}
		})
	}
}
