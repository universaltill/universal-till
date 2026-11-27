package enroll

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// Hooks the self-heal engine (internal/credheal, which imports this package)
// installs, so enrolment code can talk to it without an import cycle
// (ADR-0222 §4.1). Both are optional and nil-safe: with no engine running
// (tests, the demo till) nothing happens.

// deadCredentialHook: a caller in this package got a 401 envelope (source,
// code, data.reason, data.recover) for its cloud call.
var deadCredentialHook atomic.Pointer[func(source, code, reason string, recover bool)]

// earlyDead is the first dead-credential answer that arrived before the
// engine was installed (the approval-key report at start-up runs before the
// engine exists). SetDeadCredentialHook replays it once, so a till with a dead
// credential and no cloud plan does not wait for the next retry 15 minutes
// later; the engine probes with a real check-in first, so a stray 401 never
// rotates anything (ADR-0222 as amended, ADR-0148).
var earlyDead struct {
	sync.Mutex
	set                  bool
	at                   time.Time
	source, code, reason string
	recover              bool
}

// earlyDeadMaxAge keeps a signal from an engine that never started from being
// replayed much later.
const earlyDeadMaxAge = 5 * time.Minute

// SetDeadCredentialHook installs (or, with nil, removes) the engine's signal.
// Installing it replays a recent early signal once.
func SetDeadCredentialHook(f func(source, code, reason string, recover bool)) {
	if f == nil {
		deadCredentialHook.Store(nil)
		earlyDead.Lock()
		earlyDead.set = false
		earlyDead.Unlock()
		return
	}
	deadCredentialHook.Store(&f)
	earlyDead.Lock()
	had, at := earlyDead.set, earlyDead.at
	source, code, reason, rec := earlyDead.source, earlyDead.code, earlyDead.reason, earlyDead.recover
	earlyDead.set = false
	earlyDead.Unlock()
	if had && time.Since(at) < earlyDeadMaxAge {
		f(source, code, reason, rec)
	}
}

func reportDeadCredential(source, code, reason string, recover bool) {
	if f := deadCredentialHook.Load(); f != nil {
		(*f)(source, code, reason, recover)
		return
	}
	earlyDead.Lock()
	if !earlyDead.set {
		earlyDead.set, earlyDead.at = true, time.Now()
		earlyDead.source, earlyDead.code, earlyDead.reason, earlyDead.recover = source, code, reason, recover
	}
	earlyDead.Unlock()
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
