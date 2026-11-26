package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// Test-only helpers: production gates go through checkOrElevateBound
// (ut-docs#7085), so these would be dead code outside tests.

// checkOrElevateAll is checkOrElevate for a request that needs several
// actions at once (ut-docs#5416: one post that both lowers a line's qty and
// raises its discount). allowed only if the session holds EVERY action;
// otherwise ONE PIN verification, whose approver must hold every action too
// (errApproverInsufficientRole if any is missing). The PIN is checked once,
// so a wrong PIN counts one failed attempt toward the shared lockout, not one
// per action.
func checkOrElevateAll(dp *common.Deps, r *http.Request, pin string, actions ...string) elevationCheck {
	actorID := getSessionUserID(r)
	all := true
	for _, a := range actions {
		if !canPerform(dp, r, a) {
			all = false
			break
		}
	}
	if all {
		return elevationCheck{Outcome: allowed, ActorID: actorID}
	}
	return verifyElevationPIN(dp, r, actorID, pin, actions...)
}

// reset drops every approval (tests simulate a restart with it).
func (s *approvalStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = map[string]approvalEntry{}
}
