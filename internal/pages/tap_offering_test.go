package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cardreaders"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6455 G-6 (ADR-0125 s5, ADR-0198 s3.4/3.9/6.10): a tap entry is offered
// only on the main/standalone cashier till, and only while online.

func (e *tapEnv) tenderOffline(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"tapstripe","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.cashier))
	return rec
}

func (e *tapEnv) wantTapNeverStarted(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if n := e.bindingCount(); n != 0 {
		t.Fatalf("the tap plugin was asked %d times", n)
	}
	if n := e.broker.count("session_create"); n != 0 {
		t.Fatalf("session_create called %d times", n)
	}
	if e.sales(t) != 0 || e.dp.Engine.TenderLock() != "" {
		t.Fatalf("sales=%d lock=%q, want 0 and none", e.sales(t), e.dp.Engine.TenderLock())
	}
}

func TestTapOffer_MainTillOfferedOnline(t *testing.T) {
	e := tapRig(t)
	if err := tapOfferGate(context.Background(), e.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("main till online must offer a tap entry: %v", err)
	}
	pm := []data.PaymentMethod{{ID: "cash"}, {ID: "tapstripe"}}
	if got := withoutTapEntries(context.Background(), e.dp, pm); len(got) != 1 || got[0].ID != "cash" {
		t.Fatalf("withoutTapEntries = %+v, want [cash]", got)
	}
}

func TestTapOffer_KioskNeverGetsATapEntry(t *testing.T) {
	e := tapRig(t)
	var tErr *tapStartFailedError
	if err := tapOfferGate(context.Background(), e.dp, readerSourceKiosk, false, []string{"tapstripe"}); !asTapStart(err, &tErr) || tErr.Method != "tapstripe" {
		t.Fatalf("kiosk tap leg: %v, want tapStartFailedError(tapstripe)", err)
	}
	if err := tapOfferGate(context.Background(), e.dp, readerSourceKiosk, false, []string{"cash"}); err != nil {
		t.Fatalf("kiosk cash leg must pass: %v", err)
	}
	if tapOfferedOn(readerSourceKiosk) {
		t.Fatal("kiosk source must not be offered a tap")
	}
}

func asTapStart(err error, target **tapStartFailedError) bool {
	e, ok := err.(*tapStartFailedError)
	if ok {
		*target = e
	}
	return ok
}

// joinTill makes the rig a joined till with this explicit role (setting
// sync.till_role); role "" leaves the role unrecorded (no setting, no roster row).
func (e *tapEnv) joinTill(t *testing.T, role string) {
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

// A satellite, or a joined till whose role is not explicitly recorded, never
// offers a tap: no row in the picker, no tap button, and a tender naming one is
// refused before the plugin (ADR-0198 Amendment 2026-11-23, fail closed).
func TestTapOffer_SatelliteAndUnrecordedRoleNeverGetATapEntry(t *testing.T) {
	for _, role := range []string{data.TillRoleSatellite, ""} {
		t.Run("role="+role, func(t *testing.T) {
			e := tapRig(t)
			ctx := context.Background()
			e.joinTill(t, role)
			if role == "" {
				// ownTillRole falls back to additional for the UI; the money gate must not.
				if got := ownTillRole(ctx, e.dp); got != data.TillRoleAdditional {
					t.Fatalf("ownTillRole fallback = %q, want additional (unchanged)", got)
				}
			}
			if got := effectiveReaderSource(ctx, e.dp, readerSourcePOS); got != readerSourceJoined {
				t.Fatalf("effective source on a %q till = %q, want %q", role, got, readerSourceJoined)
			}
			rec := e.tender(t)
			e.wantText(t, rec, "tap.start_failed")
			e.wantTapNeverStarted(t, rec)
			if got := withoutTapEntries(ctx, e.dp, []data.PaymentMethod{{ID: "tapstripe"}, {ID: "cash"}}); len(got) != 1 {
				t.Fatalf("grid keeps %+v", got)
			}
			cardreaders.TapToPayAvailable = func() bool { return true }
			defer func() { cardreaders.TapToPayAvailable = func() bool { return false } }()
			row := data.CardReader{Kind: data.CardReaderKindTapToPay, Transport: data.CardReaderTransportDeviceOwn}
			if readerEligible(row, effectiveReaderSource(ctx, e.dp, readerSourcePOS)) {
				t.Fatalf("a tap_to_pay row is eligible on a %q till", role)
			}
			if !readerEligible(row, readerSourcePOS) {
				t.Fatal("a tap_to_pay row must stay eligible on the main till with a bridge")
			}
		})
	}
}

// B1: an additional till with an explicit role, online and with a ready device,
// offers the tap entry (ADR-0198 Amendment 2026-11-23).
func TestTapOffer_AdditionalTillOfferedOnlineWhenReady(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	e.joinTill(t, data.TillRoleAdditional)
	if got := effectiveReaderSource(ctx, e.dp, readerSourcePOS); got != readerSourceAdditional {
		t.Fatalf("effective source = %q, want %q", got, readerSourceAdditional)
	}
	if !tapOfferedOn(readerSourceAdditional) {
		t.Fatal("tapOfferedOn(additional) = false, want true")
	}
	if err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("additional till online and ready must offer tap: %v", err)
	}
	pm := []data.PaymentMethod{{ID: "cash"}, {ID: "tapstripe"}}
	if got := withoutUnreadyTapEntries(ctx, e.dp, pm); len(got) != 2 {
		t.Fatalf("withoutUnreadyTapEntries = %+v, want both", got)
	}
	// The roster row counts as explicit too (no setting).
	e2 := tapRig(t)
	if err := e2.dp.Settings.Set(ctx, "sync.primary_url", "http://main.example"); err != nil {
		t.Fatal(err)
	}
	if err := e2.dp.Settings.Set(ctx, "sync.till_id", "t-add"); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.dp.Db.Exec(`INSERT INTO tills(id, name, bearer_hash, role) VALUES ('t-add','Add','h','additional')`); err != nil {
		t.Skipf("tills roster insert unsupported here: %v", err)
	}
	if got := effectiveReaderSource(ctx, e2.dp, readerSourcePOS); got != readerSourceAdditional {
		t.Fatalf("roster-explicit additional: source = %q, want %q", got, readerSourceAdditional)
	}
}

// B2: an offline sale on an additional till never starts a tap.
func TestTapOffer_AdditionalTillOfflineSaleNeverStartsATap(t *testing.T) {
	e := tapRig(t)
	e.joinTill(t, data.TillRoleAdditional)
	rec := e.tenderOffline(t)
	e.wantText(t, rec, "tap.start_failed")
	e.wantTapNeverStarted(t, rec)
}

// D8: an additional till takes only its own device's Tap to Pay; a paired phone
// reader never counts, so with no ready device the entry is refused NotReady.
func TestTapOffer_AdditionalTillIgnoresPairedPhoneReader(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	devicepay.SetBridge(&fakeTapBridge{caps: capsNeedsTerms})
	e.joinTill(t, data.TillRoleAdditional)
	if _, err := data.NewCardReaderRepo(e.dp.Db).Create(ctx, data.CardReader{
		Name: "Phone", MethodKey: "tapstripe", PluginID: "p.tap", ReaderRef: "ref-1",
		Kind: data.CardReaderKindPhone, Source: data.CardReaderSourcePaired, Transport: data.CardReaderTransportInternet,
	}); err != nil {
		t.Fatalf("seed phone reader: %v", err)
	}
	var tErr *tapStartFailedError
	err := tapOfferGate(ctx, e.dp, readerSourcePOS, false, []string{"tapstripe"})
	if !asTapStart(err, &tErr) || !tErr.NotReady {
		t.Fatalf("additional till with only a phone reader: %v, want NotReady", err)
	}
	// The same reader still counts on the main till.
	e2 := tapRig(t)
	devicepay.SetBridge(&fakeTapBridge{caps: capsNeedsTerms})
	if _, err := data.NewCardReaderRepo(e2.dp.Db).Create(ctx, data.CardReader{
		Name: "Phone", MethodKey: "tapstripe", PluginID: "p.tap", ReaderRef: "ref-1",
		Kind: data.CardReaderKindPhone, Source: data.CardReaderSourcePaired, Transport: data.CardReaderTransportInternet,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tapOfferGate(ctx, e2.dp, readerSourcePOS, false, []string{"tapstripe"}); err != nil {
		t.Fatalf("main till with a phone reader: %v", err)
	}
}

func TestTapOffer_OfflineSaleNeverStartsATap(t *testing.T) {
	e := tapRig(t)
	rec := e.tenderOffline(t)
	e.wantText(t, rec, "tap.start_failed")
	e.wantTapNeverStarted(t, rec)
}

// A plugin-list read that fails must not let a tap leg through a surface that
// never offers one: the gate cannot prove the leg is not a tap, so it refuses.
func TestTapOffer_FailedPluginReadRefusesOnAKiosk(t *testing.T) {
	e := tapRig(t)
	if err := e.dp.Db.Close(); err != nil {
		t.Fatal(err)
	}
	var tErr *tapStartFailedError
	if err := tapOfferGate(context.Background(), e.dp, readerSourceKiosk, false, []string{"tapstripe"}); !asTapStart(err, &tErr) {
		t.Fatalf("kiosk leg with an unreadable plugin list: %v, want tapStartFailedError", err)
	}
}

// UX copy section 4: a till whose credential the cloud refused as
// role-unrecorded says "set this till up again" instead of the generic start
// failure; nothing was charged.
func TestTapGate_RoleUnrecordedStartFailureSaysSetUpAgain(t *testing.T) {
	for _, code := range []string{"device_role_unrecorded", "device_role_unconfirmed"} {
		t.Run(code, func(t *testing.T) { roleStartFailure(t, code) })
	}
}

// clearRoleRefusal forgets a remembered role-unrecorded refusal the way
// production does: one successful broker call clears it (no test-only reset
// hook in non-test code, which the deadcode baseline would refuse).
func clearRoleRefusal(e *tapEnv) {
	e.broker.setRoleRefused(false)
	_, _ = paybroker.Call(context.Background(), paybroker.CallerCore, "stripe", "session_create", []byte(`{}`))
}

func roleStartFailure(t *testing.T, code string) {
	e := tapRig(t)
	e.broker.setRoleCode(code)
	clearRoleRefusal(e)
	t.Cleanup(func() { clearRoleRefusal(e) })
	e.joinTill(t, data.TillRoleAdditional)
	e.noRow = true
	e.plugin.answer = func(int) string { return "START_FAILED" }
	e.broker.setRoleRefused(true)
	if _, err := paybroker.Call(context.Background(), paybroker.CallerCore, "stripe", "session_create", []byte(`{}`)); err == nil {
		t.Fatal("fake cloud should have refused")
	}
	rec := e.tender(t)
	e.wantText(t, rec, "tap.start_failed.setup_again", "tap.start_failed.setup_again_hint")
	if strings.Contains(bodyText(rec), "device_role_unrecorded") {
		t.Fatal("the cloud code is never shown")
	}
	if e.sales(t) != 0 || e.dp.Engine.TenderLock() != "" {
		t.Fatalf("a start failure moves no money and must not lock: lock=%q", e.dp.Engine.TenderLock())
	}
}

// D9 / B6: an additional till never sends a refund of a card tap: the refund
// page answers 409 with the main-till wording and the plugin is never asked.
func TestTapRefund_AdditionalTillRefusesAndSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		refused    bool
	}{{"additional", data.TillRoleAdditional, true}, {"main", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRefCardRig(t, func(int) string { return `"ch_TESTREF1234"` }, "")
			r.sell(t, `[{"method":"demopay","amount":120}]`)
			receipt := r.receiptNo(t, 1)
			// From here the method is a tap entry (config only; the sale is done).
			if _, err := r.dp.Db.Exec(`UPDATE plugin_entries SET config_json = '{"tap":true,"provider":"stripe"}' WHERE key = 'demopay'`); err != nil {
				t.Fatal(err)
			}
			if tc.role != "" {
				ctx := context.Background()
				_ = r.dp.Settings.Set(ctx, "sync.primary_url", "http://main.example")
				_ = r.dp.Settings.Set(ctx, tillRoleSettingsKey, tc.role)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receipt+"&qty_0=1&method=demopay"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			r.mux.ServeHTTP(rec, req)
			if tc.refused {
				if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), enText(t, "tap.refund.main_only")) {
					t.Fatalf("refund on an additional till = %d %s, want 409 with tap.refund.main_only", rec.Code, rec.Body.String())
				}
				if n := len(r.refunds()); n != 0 {
					t.Fatalf("the plugin was asked to refund %d times", n)
				}
				return
			}
			if rec.Code >= 400 || len(r.refunds()) != 1 {
				t.Fatalf("main till refund = %d, refunds=%d, want success and 1", rec.Code, len(r.refunds()))
			}
		})
	}
}

// D9: automatic reversal of a captured tap leg on an additional till cannot
// refund: it counts as failed, sends nothing, and the abandon toast names the
// main till. The main till still reverses.
func TestTapReversal_AdditionalTillFailsWithMainTillWording(t *testing.T) {
	leg := pos.CapturedLeg{MethodID: "tapstripe", Currency: "GBP", RequestID: "att:0:abcd", Amount: money.FromMinor(120), TransactionRef: "pi_TESTREF9876"}
	e := tapRig(t)
	if f, m := reversePaymentLegsDetail(context.Background(), e.dp, []pos.CapturedLeg{leg}, "test"); f != 0 || m != 0 {
		t.Fatalf("main till reversal failed=%d mainOnly=%d, want 0 0", f, m)
	}
	if n := len(e.plugin.refundCalls()); n != 1 {
		t.Fatalf("main till refund calls = %d, want 1", n)
	}
	e2 := tapRig(t)
	e2.joinTill(t, data.TillRoleAdditional)
	if f, m := reversePaymentLegsDetail(context.Background(), e2.dp, []pos.CapturedLeg{leg}, "test"); f != 1 || m != 1 {
		t.Fatalf("additional till reversal failed=%d mainOnly=%d, want 1 1", f, m)
	}
	if n := len(e2.plugin.refundCalls()); n != 0 {
		t.Fatalf("additional till sent %d refunds, want 0", n)
	}
	if got := abandonToastKeyFor(pos.AbandonResult{Failed: 1, MainOnly: 1}); got != "tap.reverse.main_only" {
		t.Fatalf("toast key = %q, want tap.reverse.main_only", got)
	}
	if got := abandonToastKeyFor(pos.AbandonResult{Failed: 1}); got != "pos.toast.tender_abandoned_check_provider" {
		t.Fatalf("toast key = %q (unchanged path)", got)
	}
}

// Review finding: a joined till in self-order display mode reports "kiosk" to
// the cloud, which narrows its credential; the till must not offer a tap the
// cloud will refuse, even though its roster/setting still says additional.
func TestTapOffer_AdditionalTillInSelfOrderModeIsAKioskNoTap(t *testing.T) {
	e := tapRig(t)
	ctx := context.Background()
	e.joinTill(t, data.TillRoleAdditional)
	if err := e.dp.Settings.Set(ctx, "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	if got := effectiveReaderSource(ctx, e.dp, readerSourcePOS); got != readerSourceJoined {
		t.Fatalf("effective source = %q, want %q", got, readerSourceJoined)
	}
	e.wantText(t, e.tender(t), "tap.start_failed")
}
