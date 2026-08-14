package pages

import (
	"net/http"
	"os"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// canPerform reports whether the current request's operator is granted
// action, per d.AuthSvc.Can() and the #554 role_permissions catalog. This
// replaces the blanket isManagerOrAuthOff gate one subsystem at a time
// (#555, split across 5 successor cards so each diff stays reviewable) —
// isManagerOrAuthOff itself stays in place until every call site has moved.
//
// Same UT_AUTH=off escape hatch as isManagerOrAuthOff: with auth disabled
// there is no session to check, so dev/CI tooling passes. Fails closed
// otherwise — no session, an unknown user/action pairing, or a DB error all
// deny, matching AuthRepo.HasPermission's own closed-permission model.
//
// Note: Can() also grants the super_admin role (seeded in #554), while
// isManagerOrAuthOff/User.IsManager() only recognize manager/admin — a real
// broadening versus today's gate. It's inert for every existing till: no
// code path creates a super_admin-role user yet (see #555's tracking
// comment), so no real till's access changes from this swap.
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
