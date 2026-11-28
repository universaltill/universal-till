package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0215 (ut-docs#6581): the delivery order hook: unmapped-line policy
// setting, auto reject, late-cancel Problem, and nothing outside the order.

func deliveryHookOrder(t *testing.T, unmapped bool) cloudsync.ChannelOrderDirective {
	t.Helper()
	o, _ := decodeFixtureForHook(t)
	o.ExternalOrderID, o.OrderNumber = "d-77", "#D77"
	o.Lines = o.Lines[:1]
	o.Lines[0].CloudItemID, o.Lines[0].SKU = "co-itm", "CO-1"
	o.Shipping, o.Total = 0, money.FromMinor(7000)
	o.Delivery = &data.ChannelDeliveryInput{Service: "delivery", AcceptBy: "2031-12-01T12:10:00Z"}
	if unmapped {
		o.Lines = append(o.Lines, data.ChannelOrderLineInput{LineID: "l-9", SKU: "NOPE", Name: "Mystery", Qty: 1})
	}
	return o
}

func deliveryHookDeps(t *testing.T) *common.Deps {
	t.Helper()
	dp := newCloudSyncTestDeps(t)
	dp.CloudSyncNow = make(chan struct{}, 1)
	for _, q := range []string{
		`INSERT INTO tax_codes(id,name,rate_basis_points,is_active) VALUES('tc-co','Std',2000,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('co-itm','CO-1','Shirt',3500,'tc-co',1)`,
		`INSERT INTO inventory(id,item_id,location_id,quantity) VALUES('co-inv','co-itm','loc_main',10)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return dp
}

func stockOnHand(t *testing.T, dp *common.Deps) float64 {
	t.Helper()
	var q float64
	if err := dp.Db.QueryRow(`SELECT quantity FROM inventory WHERE id='co-inv'`).Scan(&q); err != nil {
		t.Fatal(err)
	}
	return q
}

func TestCloudApplyDelivery_UnmappedLineRejectedByDefault(t *testing.T) {
	dp := deliveryHookDeps(t)
	if _, err := cloudApplyChannelOrder(t.Context(), dp, deliveryHookOrder(t, true)); err != nil {
		t.Fatal(err)
	}
	if q := stockOnHand(t, dp); q != 10 {
		t.Fatalf("stock = %v, a rejected order moves none", q)
	}
	var action, reason string
	if err := dp.Db.QueryRow(`SELECT action, reject_reason FROM channel_order_actions WHERE state='pending'`).Scan(&action, &reason); err != nil ||
		action != "reject" || reason != "item_unavailable" {
		t.Fatalf("queued reject = %q %q %v", action, reason, err)
	}
	// An online till is asked to check in at once so the reject goes out.
	select {
	case <-dp.CloudSyncNow:
	default:
		t.Fatal("the auto reject must kick a check-in")
	}
}

func TestCloudApplyDelivery_PlaceholderPolicySettingKeepsTheOrder(t *testing.T) {
	dp := deliveryHookDeps(t)
	if err := dp.Settings.Set(t.Context(), data.DeliveryUnmappedPolicyKey, data.DeliveryUnmappedPlaceholder); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudApplyChannelOrder(t.Context(), dp, deliveryHookOrder(t, true)); err != nil {
		t.Fatal(err)
	}
	if q := stockOnHand(t, dp); q != 8 {
		t.Fatalf("stock = %v, want 8", q)
	}
	var n int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM channel_order_actions`).Scan(&n)
	if n != 0 {
		t.Fatalf("actions = %d", n)
	}
}

func TestCloudApplyDelivery_UnknownPolicyValueFailsClosedToReject(t *testing.T) {
	dp := deliveryHookDeps(t)
	if err := dp.Settings.Set(t.Context(), data.DeliveryUnmappedPolicyKey, "whatever"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudApplyChannelOrder(t.Context(), dp, deliveryHookOrder(t, true)); err != nil {
		t.Fatal(err)
	}
	if q := stockOnHand(t, dp); q != 10 {
		t.Fatalf("stock = %v", q)
	}
}

func TestCloudApplyDelivery_LateCancelRaisesAProblemAndIsNeverRefused(t *testing.T) {
	// The Problems ring is process-global: start clean so another test's
	// Problem (any order, -shuffle) is never read as this one's.
	logging.ResetRecent()
	dp := deliveryHookDeps(t)
	o := deliveryHookOrder(t, false)
	o.Status = "preparing"
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	o.Revision, o.Status = 2, "cancelled"
	o.Lines[0].RefundedQuantity, o.Lines[0].RestockedQuantity = 2, 2
	o.RefundTotal = o.Total
	msg, err := cloudApplyChannelOrder(t.Context(), dp, o)
	if err != nil {
		t.Fatalf("a cancel is a fact and is never refused: %v", err)
	}
	if !strings.Contains(msg, "#D77") {
		t.Fatalf("message = %q", msg)
	}
	var all []string
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		all = append(all, p.Msg)
	}
	if text := strings.Join(all, "\n"); !strings.Contains(text, "late cancel") || !strings.Contains(text, "#D77") {
		t.Fatalf("no late-cancel Problem in:\n%s", text)
	}
	for _, leak := range []string{"customer", "phone", "address"} {
		for _, m := range all {
			if strings.Contains(strings.ToLower(m), leak) {
				t.Fatalf("problem text mentions %q: %s", leak, m)
			}
		}
	}
}

func TestCloudApplyDelivery_NoteIsNeverLogged(t *testing.T) {
	// The Problems ring is process-global: start clean so another test's
	// Problem (any order, -shuffle) is never read as this one's.
	logging.ResetRecent()
	dp := deliveryHookDeps(t)
	o := deliveryHookOrder(t, true)
	o.Note = "SECRETNOTE severe peanut allergy"
	if _, err := cloudApplyChannelOrder(t.Context(), dp, o); err != nil {
		t.Fatal(err)
	}
	for _, p := range logging.OpenProblems(time.Now(), 0) {
		if strings.Contains(p.Msg, "SECRETNOTE") {
			t.Fatalf("the customer note reached a Problem: %s", p.Msg)
		}
	}
}
