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

// A joined till (satellite or additional) never offers a tap: no row in the
// picker, no tap button, and a tender naming one is refused before the plugin.
func TestTapOffer_SatelliteAndAdditionalTillsNeverGetATapEntry(t *testing.T) {
	for _, role := range []string{data.TillRoleSatellite, data.TillRoleAdditional} {
		t.Run(role, func(t *testing.T) {
			e := tapRig(t)
			ctx := context.Background()
			if err := e.dp.Settings.Set(ctx, "sync.primary_url", "http://main.example"); err != nil {
				t.Fatal(err)
			}
			if err := e.dp.Settings.Set(ctx, tillRoleSettingsKey, role); err != nil {
				t.Fatal(err)
			}
			if got := effectiveReaderSource(ctx, e.dp, readerSourcePOS); got != readerSourceJoined {
				t.Fatalf("effective source on a %s till = %q, want %q", role, got, readerSourceJoined)
			}
			rec := e.tender(t)
			e.wantText(t, rec, "tap.start_failed")
			e.wantTapNeverStarted(t, rec)
			if got := withoutTapEntries(ctx, e.dp, []data.PaymentMethod{{ID: "tapstripe"}, {ID: "cash"}}); len(got) != 1 {
				t.Fatalf("grid keeps %+v", got)
			}
			// the reader row of kind tap_to_pay is not eligible there either.
			cardreaders.TapToPayAvailable = func() bool { return true }
			defer func() { cardreaders.TapToPayAvailable = func() bool { return false } }()
			row := data.CardReader{Kind: data.CardReaderKindTapToPay, Transport: data.CardReaderTransportDeviceOwn}
			if readerEligible(row, effectiveReaderSource(ctx, e.dp, readerSourcePOS)) {
				t.Fatalf("a tap_to_pay row is eligible on a %s till", role)
			}
			if !readerEligible(row, readerSourcePOS) {
				t.Fatal("a tap_to_pay row must stay eligible on the main till with a bridge")
			}
		})
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
