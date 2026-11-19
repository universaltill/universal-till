package pages

import (
	"context"
	"database/sql"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// payBrokerTarget is where internal/paybroker sends a payment-broker call
// (ut-docs#3049): the live enrolment's cloud endpoint and this till's
// ADR-0116 device credential, read per call through enroll.Effective like
// cloudLinkGate, so a till that enrols or rotates its token after boot
// needs no restart. No complete enrolment → no target (ErrNotEnrolled).
// The cloud derives the store from the credential, never from the body.
func payBrokerTarget(cfg *config.Config) (paybroker.Target, bool) {
	if cfg == nil {
		return paybroker.Target{}, false
	}
	m := enroll.Effective(cfg).Marketplace
	if !enroll.CredentialsComplete(m) {
		return paybroker.Target{}, false
	}
	return paybroker.Target{BaseURL: m.EndpointURL, Bearer: m.MerchantToken}, true
}

// wirePayBroker installs payBrokerTarget as the broker's resolver.
func wirePayBroker(cfg *config.Config) {
	paybroker.SetTargetResolver(func(context.Context) (paybroker.Target, bool) {
		return payBrokerTarget(cfg)
	})
}

// wirePayAttempts tells the broker how to read core's own in-flight attempt
// (ut-docs#6455): the native shell's session_create is accepted only when it
// equals an open payment_attempts row core wrote before the tap. A row that
// is not pending, or that cannot be read, is no attempt.
func wirePayAttempts(db *sql.DB) {
	repo := data.NewPaymentAttemptRepo(db)
	paybroker.SetAttemptLookup(func(ctx context.Context, key string) (paybroker.AttemptView, bool) {
		a, ok, err := repo.Get(ctx, key)
		if err != nil || !ok {
			return paybroker.AttemptView{}, false
		}
		return paybroker.AttemptView{
			Key: a.Key, Provider: a.Provider, AmountMinor: a.AmountMinor, Currency: a.Currency,
			Pending: a.State == data.AttemptPending,
		}, true
	})
}
