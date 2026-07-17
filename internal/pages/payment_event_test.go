package pages

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// The blocking payment-provider gate (payment.<key>.authorize/.refund): the
// method's owning plugin approves (nil), declines (error propagates and the
// caller must stop the sale/refund), and methods without an entry or a
// subscriber pass through untouched (cash stays cash).
func TestBlockingPaymentEventGate(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	// A payment plugin with a payment entry: method "stripe", trigger
	// payment.stripe.requested (the contract's async leg).
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO plugins (id, name, version, is_active) VALUES ('com.universaltill.payment-stripe', 'Stripe', '1.1.1', 1)`)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.payment-stripe', 'stripe', 'Card (Stripe)', 'payment', 'payment.stripe.requested', 1)`)
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.payment-stripe', 'events:receive', 1)`)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.payment-stripe', 'payment.stripe.refund', 'handle_refund', 1)`)

	d := &common.Deps{Db: db}
	ctx := context.Background()
	bus := plugins.SharedBus(db)

	// No subscriber yet → no gate.
	if err := blockingPaymentEvent(ctx, d, "stripe", "refund", map[string]any{"amount": int64(100)}); err != nil {
		t.Fatalf("no-subscriber gate: %v", err)
	}

	// Subscribe a blocking handler that approves, then declines.
	decline := false
	bus.SetEventMode("payment.stripe.refund", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(ctx, "com.universaltill.payment-stripe",
		[]string{"payment.stripe.refund"},
		func(ctx context.Context, ev plugins.Event) error {
			if decline {
				return errors.New("stripe: refund failed")
			}
			return nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	if err := blockingPaymentEvent(ctx, d, "stripe", "refund", map[string]any{"amount": int64(100)}); err != nil {
		t.Fatalf("approving provider should not block: %v", err)
	}
	decline = true
	if err := blockingPaymentEvent(ctx, d, "stripe", "refund", map[string]any{"amount": int64(100)}); err == nil {
		t.Fatal("declining provider must block the refund")
	}
	// A different method (cash) has no entry — never gated.
	if err := blockingPaymentEvent(ctx, d, "cash", "refund", map[string]any{"amount": int64(100)}); err != nil {
		t.Fatalf("cash must not be gated: %v", err)
	}
}
