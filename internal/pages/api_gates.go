package pages

import (
	"encoding/json"
	"net/http"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// The replica-side discovery/pairing surface exists in two authorization
// flavours (ut-docs#289): manager-gated on the Tills page of a configured
// till (/api/sync/*), and first-boot-gated on the setup wizard of a
// brand-new till (/api/setup/*). The handler logic is identical, so each
// handler is written once and parameterized by an apiGate; only the gate
// differs between the two route registrations.

// apiGate authorizes a request for one of those handlers. It returns true
// to let the handler run; on refusal it writes the whole response itself
// (403 for the manager flavour, a redirect to /login for the first-boot
// flavour — mirroring their respective precedents, /api/sync/join and
// /api/setup/join in sync_api.go).
type apiGate func(w http.ResponseWriter, r *http.Request) bool

// managerGate is the Tills-page check every /api/sync sibling uses — the
// same sync_management Can() check as sync_api.go/pairing_api.go/
// pending_pairings.go (ut-docs#707), just packaged as an apiGate so
// discovery_api.go and pairing_join.go can parameterize a handler by gate
// like firstBootGate below.
func managerGate(d *common.Deps) apiGate {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if !canPerform(d, r, "sync_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return false
		}
		return true
	}
}

// firstBootGate admits requests only while NO operator with a PIN exists —
// the same monotonic window as /api/setup/join. These routes are
// middleware-exempt (internal/auth/middleware.go), so this check is the
// ONLY thing keeping an unauthenticated LAN caller off a configured till;
// treat any change here as security-critical
// (TestSetup*FirstBootGate in setup_pairing_test.go pin the behaviour).
func firstBootGate(d *common.Deps) apiGate {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if firstBoot, err := d.AuthSvc.NeedsFirstBoot(r.Context()); err != nil || !firstBoot {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return false
		}
		return true
	}
}

// rateLimited wraps a gate with a per-source cap, checked BEFORE the
// wrapped gate — independent review (ut-docs#289) found the first-boot
// discover-primaries (a 4s mDNS multicast scan) and pair-start (an
// unauthenticated outbound-HTTP primitive to any attacker-chosen host) had
// no limiter, unlike pairing_api.go's inbound pair-request/token-retrieval
// endpoints which reuse the exact same pairRateLimiter+sourceOf for this
// reason. The manager-gated /api/sync equivalents stay unlimited (a manager
// session is already the higher bar); only the lower-trust, LAN-reachable-
// by-anyone first-boot flavour gets this.
func rateLimited(limiter *pairRateLimiter, gate apiGate) apiGate {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if !limiter.allow(sourceOf(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": "too many requests"})
			return false
		}
		return gate(w, r)
	}
}
