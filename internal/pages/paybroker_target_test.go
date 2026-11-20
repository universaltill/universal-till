package pages

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// ut-docs#3049: the payment broker calls the cloud with the live enrolment
// (endpoint + ADR-0116 device credential), resolved per call like the cloud
// link's target, and refuses when the till holds no complete enrolment.
func TestPayBrokerTarget(t *testing.T) {
	cfg := func(endpoint, store, token string) *config.Config {
		c := &config.Config{}
		c.Marketplace.EndpointURL = endpoint
		c.Marketplace.StoreID = store
		c.Marketplace.MerchantToken = token
		return c
	}
	tg, ok := payBrokerTarget(cfg("https://cloud.example.test/api", "store-1", "cred-1"))
	if !ok || tg.BaseURL != "https://cloud.example.test/api" || tg.Bearer != "cred-1" {
		t.Fatalf("enrolled: %+v ok=%v", tg, ok)
	}
	if _, ok := payBrokerTarget(nil); ok {
		t.Fatal("nil config: want no target")
	}
	for name, c := range map[string]*config.Config{
		"no token":    cfg("https://cloud.example.test/api", "store-1", ""),
		"no endpoint": cfg("", "store-1", "cred-1"),
		"no store":    cfg("https://cloud.example.test/api", "", "cred-1"),
	} {
		if tg, ok := payBrokerTarget(c); ok {
			t.Fatalf("%s: got target %+v, want none", name, tg)
		}
	}
}

// ut-docs#6455: the broker reads the in-flight attempt from payment_attempts;
// only a pending row counts, so a settled or unknown one cannot be re-created.
func TestWirePayAttempts(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	wirePayAttempts(database.DB)
	t.Cleanup(func() { paybroker.SetAttemptLookup(nil) })
	ctx := context.Background()
	repo := data.NewPaymentAttemptRepo(database.DB)
	if _, err := repo.Begin(ctx, data.PaymentAttempt{Key: "k1", Provider: "stripe", AmountMinor: 500, Currency: "gbp"}); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"idempotency_key":"k1","amount_minor":500,"currency":"gbp"}`)
	paybroker.SetTargetResolver(nil)
	// Accepted by the attempt check (then stops at "not enrolled").
	if _, err := paybroker.Call(ctx, paybroker.CallerNative, "stripe", "session_create", body); !errors.Is(err, paybroker.ErrNotEnrolled) {
		t.Fatalf("pending attempt: err = %v, want to pass the attempt check", err)
	}
	if _, err := paybroker.Call(ctx, paybroker.CallerNative, "stripe", "session_create", []byte(`{"idempotency_key":"k1","amount_minor":501,"currency":"gbp"}`)); !errors.Is(err, paybroker.ErrNoAttempt) {
		t.Fatalf("other amount: err = %v, want ErrNoAttempt", err)
	}
	if err := repo.Mark(ctx, "k1", data.AttemptUnknown, data.AttemptUpdate{}); err != nil {
		t.Fatal(err)
	}
	if _, err := paybroker.Call(ctx, paybroker.CallerNative, "stripe", "session_create", body); !errors.Is(err, paybroker.ErrNoAttempt) {
		t.Fatalf("unknown attempt: err = %v, want ErrNoAttempt", err)
	}
}
