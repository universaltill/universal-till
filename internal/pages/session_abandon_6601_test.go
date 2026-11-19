package pages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6601: a table-QR session that expires with a captured leg gets it
// reversed, and the reversal request id is stable.

func TestAbandon_TableSessionSweepReversesCapturedLeg(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	dp.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service {
		return pos.NewServiceWithResolver(dp.Engine.Config(), nil)
	})
	wireAbandonedTenderReversal(dp)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, firstApprovedThenUnknown)

	_, svc, ok := dp.SelfOrderSessions.Create()
	if !ok {
		t.Fatal("Create refused")
	}
	svc.LockTender("sig", pos.PendingTender{
		Captured: []pos.CapturedLeg{{MethodID: "demopay", Currency: "EUR", RequestID: "att:0:abcd", Amount: money.FromMinor(60), TransactionRef: "ch_TESTREF9876"}},
		Unknown:  []pos.UnknownLeg{{MethodID: "demopay", Currency: "EUR", RequestID: "att:1:ef01", Amount: money.FromMinor(60)}},
	})

	selfOrderSessionSweepTick(dp, time.Now().Add(3*time.Hour))

	got := refunds()
	if len(got) != 1 || got[0]["reversal"] != true || got[0]["authorize_request_id"] != "att:0:abcd" {
		t.Fatalf("want exactly one reversal of att:0:abcd, got %+v", got)
	}
	var all []string
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		all = append(all, p.Msg)
	}
	text := strings.Join(all, "\n")
	if !strings.Contains(text, "UNKNOWN outcome") || !strings.Contains(text, "table order session expired") {
		t.Fatalf("Problems must name the unknown leg and the expiry:\n%s", text)
	}
}

func TestReversal_RequestIDDerivedFromAuthorize(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := lockTwoLegTender(t, mux, dp, nil)
	abandonPost(t, mux, "/api/pos/reset", nil)
	got := refunds()
	if len(got) != 1 {
		t.Fatalf("want one reversal, got %+v", got)
	}
	if want := "reversal:" + authIDs()[0]; got[0]["_event_id"] != want {
		t.Fatalf("reversal event id = %v, want %q", got[0]["_event_id"], want)
	}
}

func TestReversal_SameLegTwiceSameRequestID(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, firstApprovedThenUnknown)
	leg := pos.CapturedLeg{MethodID: "demopay", Currency: "EUR", RequestID: "att:0:abcd", Amount: money.FromMinor(60)}
	for i := 0; i < 2; i++ {
		if f := reversePaymentLegs(context.Background(), dp, []pos.CapturedLeg{leg}, "test"); f != 0 {
			t.Fatalf("reversal %d failed", i)
		}
	}
	got := refunds()
	if len(got) != 2 || got[0]["_event_id"] == "" || got[0]["_event_id"] != got[1]["_event_id"] {
		t.Fatalf("event ids differ across re-sends: %+v", got)
	}
}
