package pages

import (
	"net/http"
	"os"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// canPerform reports whether the current request's operator is granted
// action, per d.AuthSvc.Can() and the #554 role_permissions catalog. This
// replaced the blanket isManagerOrAuthOff gate one subsystem at a time
// (#555, split across 5 successor cards so each diff stayed reviewable) —
// isManagerOrAuthOff had zero remaining call sites once the last successor
// (#713) merged, and was removed (#721).
//
// Same UT_AUTH=off escape hatch the old gate had: with auth disabled
// there is no session to check, so dev/CI tooling passes. Fails closed
// otherwise — no session, an unknown user/action pairing, or a DB error all
// deny, matching AuthRepo.HasPermission's own closed-permission model.
//
// Note: Can() also grants the super_admin role, while the old gate/
// User.IsManager() only recognized manager/admin — a real broadening
// versus the gate it replaced. This USED to be inert for every existing
// till, since no code path could create a super_admin-role user (see
// #555's tracking comment) — no longer true since ut-docs#761 added the
// promotion path, which is exactly why IsManager() itself was updated in
// that same change to also recognize super_admin (auth/service.go):
// otherwise promoting an operator to the highest role would have silently
// stripped them of every IsManager()-gated page.
func canPerform(d *common.Deps, r *http.Request, action string) bool {
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return true
	}
	u, ok := auth.FromContext(r.Context())
	if !ok {
		return false
	}
	can, err := d.AuthSvc.Can(r.Context(), u, action)
	if err != nil {
		return false
	}
	return can
}
