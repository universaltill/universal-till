package enroll

import (
	"context"
	"regexp"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/logging"
)

// Rotation onto a per-device credential (ADR-0116 D4, ut-docs#2769).
//
// A till still authenticating with its shop's shared (legacy) token gets its
// OWN device credential exactly once, in the /v1/stores/sync answer's
// data.device_id + data.device_token. The cloud never sends it again, so the
// till must keep it: it replaces marketplace.token — the key the store token
// has always lived under, which is per-till and never leaves the till (the
// admin bundle filters it both ways, the join snapshot redacts it, and the
// diagnostics stream carries no settings) — and becomes the live bearer of
// every cloud caller from the next request (they all read Effective /
// currentStoreAuth), with no restart.
//
// The token itself is never logged, in any branch below.

// rotatedTokenRe bounds what the till accepts as a bearer: today's cloud
// sends 64 hex characters; anything printable-token-shaped up to 1 KiB is
// tolerated for a future format, but never whitespace or control bytes
// (a header-injection or log-forging vector).
var rotatedTokenRe = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

const minRotatedToken, maxRotatedToken = 16, 1024

func validRotatedToken(t string) bool {
	return len(t) >= minRotatedToken && len(t) <= maxRotatedToken && rotatedTokenRe.MatchString(t)
}

var (
	// envPinWarned: the "ignored because the environment pins the token"
	// warning is logged once per process, not on every sync.
	envPinWarned atomic.Bool
	// unsavedToken: a rotated token adopted in memory whose write to
	// settings failed. The cloud does not send it again, so the next sync
	// retries the write.
	unsavedToken atomic.Bool
)

// ApplyRotatedCredential handles a sync answer's optional device_id +
// device_token pair. It adopts the token only when deviceID is exactly this
// till's own device id and the environment does not pin the token. Empty
// fields (the usual answer) change nothing. The same token again is a no-op.
// Never fails the caller: a problem is logged (without the token) and the
// till keeps its current credential.
func ApplyRotatedCredential(ctx context.Context, kv Settings, deviceID, token string) {
	log := logging.L()
	if deviceID == "" && token == "" {
		retryUnsaved(ctx, kv)
		return
	}
	if token == "" {
		return
	}
	mu.RLock()
	own, current, pinned := cur.DeviceID, cur.Token, tokenExplicit
	mu.RUnlock()
	if own == "" || deviceID != own {
		if len(deviceID) > 64 {
			deviceID = deviceID[:64] + "…"
		}
		log.Warnf("enrolment: ignored a cloud credential for device %q; this till is %q (ADR-0116 D4)", deviceID, own)
		return
	}
	if !validRotatedToken(token) {
		log.Warnf("enrolment: ignored a malformed cloud credential for this till (length %d)", len(token))
		return
	}
	if pinned {
		if envPinWarned.CompareAndSwap(false, true) {
			log.Warnf("enrolment: the cloud issued this till its own credential, but UT_MARKETPLACE_MERCHANT_TOKEN pins the token, so it was not kept; unset it (or set it to the till's own credential) before the shop's shared token is retired (ADR-0116 D4)")
		}
		return
	}
	if token == current {
		retryUnsaved(ctx, kv)
		return
	}
	// Memory first: the cloud answers this pair once, so even if the write
	// below fails, this process uses the new credential and retries saving.
	mu.Lock()
	cur.Token = token
	mu.Unlock()
	unsavedToken.Store(true)
	if err := kv.Set(ctx, keyToken, token); err != nil {
		log.Warnf("enrolment: this till's new cloud credential is in use but not yet saved (will retry on the next sync): %v", err)
		return
	}
	unsavedToken.Store(false)
	log.Infof("enrolment: this till now uses its own cloud credential (device %s, ADR-0116 D4)", own)
}

// RetryUnsavedCredential is retryUnsaved for the check-in tick, which runs
// every few minutes even when a 304 skips the sync POST: the cloud sends a
// rotated credential once, so a failed save must not wait for the next POST
// (ut-docs#2769 review S2).
func RetryUnsavedCredential(ctx context.Context, kv Settings) { retryUnsaved(ctx, kv) }

// retryUnsaved re-attempts a failed write of an adopted rotated token.
func retryUnsaved(ctx context.Context, kv Settings) {
	if !unsavedToken.Load() {
		return
	}
	mu.RLock()
	token := cur.Token
	mu.RUnlock()
	if err := kv.Set(ctx, keyToken, token); err != nil {
		logging.L().Warnf("enrolment: saving this till's cloud credential failed again (will retry on the next sync): %v", err)
		return
	}
	unsavedToken.Store(false)
}
