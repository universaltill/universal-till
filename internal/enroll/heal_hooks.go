package enroll

import (
	"context"
	"sync/atomic"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Hooks the self-heal engine (internal/credheal, which imports this package)
// installs, so enrolment code can talk to it without an import cycle
// (ADR-0217 §4.1). Both are optional and nil-safe: with no engine running
// (tests, the demo till) nothing happens.

// deadCredentialHook: a caller in this package got a 401 envelope (source,
// code, data.reason, data.recover) for its cloud call.
var deadCredentialHook atomic.Pointer[func(source, code, reason string, recover bool)]

// SetDeadCredentialHook installs (or, with nil, removes) the engine's signal.
func SetDeadCredentialHook(f func(source, code, reason string, recover bool)) {
	if f == nil {
		deadCredentialHook.Store(nil)
		return
	}
	deadCredentialHook.Store(&f)
}

func reportDeadCredential(source, code, reason string, recover bool) {
	if f := deadCredentialHook.Load(); f != nil {
		(*f)(source, code, reason, recover)
	}
}

// identityResetHook: this till's cloud identity was reset (a pairing, a new
// shop); the engine forgets its state.
var identityResetHook atomic.Pointer[func()]

// SetIdentityResetHook installs (or removes) the engine's reset.
func SetIdentityResetHook(f func()) {
	if f == nil {
		identityResetHook.Store(nil)
		return
	}
	identityResetHook.Store(&f)
}

func notifyIdentityReset() {
	if f := identityResetHook.Load(); f != nil {
		(*f)()
	}
}

// clearHeal drops the engine's saved state with the identity it belonged to.
func clearHeal(ctx context.Context, kv Settings) {
	for _, k := range []string{data.CredHealStateKey, data.CredHealReasonKey, data.CredHealDetailKey, data.CredHealSinceKey,
		data.CredHealUntilKey, data.CredHealNextAttemptAtKey, data.CredHealAttemptsKey, data.CredHealLastHealedAtKey,
		data.CredHealHealsKey, data.CredHealCodeKey, data.CredHealRefusedKey, KeyApprovalKeyConflictAt} {
		if err := kv.Set(ctx, k, ""); err != nil {
			logging.L().Warnf("enrolment: clear %s: %v", k, err)
		}
	}
	notifyIdentityReset()
}
