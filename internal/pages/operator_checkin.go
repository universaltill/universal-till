package pages

import (
	"time"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0148 §2 (ut-docs#3615): only a paid store checks in with the cloud
// periodically. An operator action on this till opens a short in-memory
// check-in window and kicks the loop, so an unpaid till learns of a paid
// plan (or a claim) without any background poll. Not opened by a
// cloud-link nudge or a main till's relayed check-in (ADR-0148 §3): those
// are not operator actions on this till. Amendment 2026-10-05
// (ut-docs#3673): a 402 plan_required closes the window (no retry until the
// next operator action or start-up), and an unpaid till also checks in once
// at start-up after an install, an update or the first start of the day
// (internal/cloudsync's start-up check-in, not this window).

const (
	// operatorCheckinWindow follows Register now, a pairing, a registering
	// plugin install, managed-TSE setup and "Check for a paid plan".
	operatorCheckinWindow = 2 * time.Minute
	// claimCheckinWindow follows a claim code: the code's own lifetime.
	claimCheckinWindow = 15 * time.Minute
)

// openOperatorWindow is the window seam; tests record the durations.
var openOperatorWindow = cloudsync.OpenOperatorWindow

// requestOperatorCheckin opens the operator check-in window for window and
// asks the loop for a check-in now. Never blocks: the kick channel holds
// one pending kick, and a nil channel (no loop) or nil deps is a no-op.
func requestOperatorCheckin(d *common.Deps, window time.Duration) {
	openOperatorWindow(window)
	if d == nil || d.CloudSyncNow == nil {
		return
	}
	select {
	case d.CloudSyncNow <- struct{}{}:
	default: // a check-in is already pending
	}
}

// checkinAfterRegistration is requestOperatorCheckin for the
// enroll.EnsureRegistered call sites: only when eff (the effective config
// the call returned) is really registered, the same three fields the
// check-in itself needs. It tests eff as given (enroll.CredentialsComplete)
// rather than re-resolving through enroll.HasCredentials(d.Cfg), so it
// judges exactly the config its caller just got back.
func checkinAfterRegistration(d *common.Deps, eff config.Config) {
	if !enroll.CredentialsComplete(eff.Marketplace) {
		return
	}
	requestOperatorCheckin(d, operatorCheckinWindow)
}
