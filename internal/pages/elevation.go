package pages

import (
	"errors"
	"net/http"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// elevationOutcome is checkOrElevate's verdict for a single request.
type elevationOutcome int

const (
	// allowed: the session user can already perform the action — proceed
	// exactly as before, actor = the session user.
	allowed elevationOutcome = iota
	// elevated: the session user could NOT perform the action, but a valid
	// approver PIN was supplied and the approver CAN — proceed as the
	// approver, and record the originally-blocked session user alongside
	// it (InsertAuditElevated).
	elevated
	// needsElevation: the session user cannot perform the action and no
	// usable approver PIN was supplied (or the one supplied failed) —
	// render the elevation prompt instead of a flat 403.
	needsElevation
)

// errApproverInsufficientRole is checkOrElevate's own Err when a PIN
// authenticates fine (AuthorizeManager's own manager/admin/super_admin role
// floor) but the resulting user's role still isn't granted the specific
// action being gated — e.g. a manager's PIN against a super_admin-only
// action like permission_management. Never wrapped from auth's own
// sentinels since this is a distinct failure mode: the PIN was genuinely
// valid, its OWNER just isn't allowed to approve this particular action.
var errApproverInsufficientRole = errors.New("approver cannot perform this action")

// errAuthServiceUnavailable is checkOrElevate's Err when dp.AuthSvc is nil
// and a PIN was actually supplied (so an auth.Service is genuinely needed to
// verify it). Currently unreachable in production — every real caller wires
// AuthSvc, and canPerform() would itself nil-panic on a nil dp.AuthSvc
// before checkOrElevate ever got this far — but this fails closed rather
// than minting a throwaway auth.Service (which would reset the shared-
// device PIN lockout counter fiscal_api.go/inventory_api.go's own
// precedents rely on being persistent), so a future change to canPerform's
// own nil-handling can't quietly turn "mint a fresh Service" into a live
// footgun.
var errAuthServiceUnavailable = errors.New("auth service unavailable for elevation")

// elevationCheck is checkOrElevate's verdict, both branches of the
// dual-attribution split (ut-docs#557) carried on one value: which session
// user was gated (ActorID, on any Outcome), and — only once Outcome ==
// elevated — which approver ended up authorizing it (ApproverID). Err is
// set exactly when a PIN WAS supplied but failed (wrong/locked/insufficient
// role), so the caller can render a real error instead of a first-time
// "PIN required" prompt (mirrors fiscal_api.go's own error branching).
type elevationCheck struct {
	Outcome    elevationOutcome
	ActorID    string // current session user (the blocked user, if not allowed)
	ApproverID string // set only when Outcome == elevated
	Err        error  // set when a PIN WAS supplied but failed
}

// checkOrElevate is the generalized manager-override-elevation gate
// (ut-docs#557): a canPerform()-gated, mutating, audit-writing handler calls
// this in place of a flat `if !canPerform(...) { 403 }`, using the SAME
// action string it already gates on. pin is whatever the request's own
// override_pin field carried (form field or JSON body — the caller decodes
// that, same content-type branching every existing handler already does;
// checkOrElevate itself never touches the request body).
//
// Modeled directly on fiscal_api.go's createTSEOverride and
// inventory_api.go's CreateNegativeInventoryOverride (their bespoke,
// pre-#557 manager-PIN-elevation precedents — both explicitly out of scope
// to modify, ADR-0052), generalized to any canPerform() action rather than
// each handler re-implementing its own PIN-lookup-then-role-recheck:
//
//   - canPerform(action) true: no elevation needed at all (allowed).
//   - canPerform false, pin == "": a first-time prompt, not a failed
//     attempt (needsElevation, Err nil).
//   - canPerform false, pin set: AuthorizeManager(pin) — the SAME
//     shared-device-lockout PIN lookup fiscal/inventory already use, so
//     this endpoint is no more of a brute-force oracle than those are.
//     ErrLockedOut/ErrInvalidPIN/any other error all deny (needsElevation,
//     Err set).
//   - AuthorizeManager succeeds: the approver's role passed AuthorizeManager's
//     OWN manager/admin/super_admin floor, but that is NOT the same as "can
//     perform THIS action" — re-checked via svc.Can(ctx, approver, action)
//     directly (not canPerform, which reads the *session* user off the
//     request context, not the approver) exactly like fiscal_api.go
//     re-checks the PIN owner's role before trusting it. A manager's PIN
//     against a super_admin-only action (e.g. permission_management) fails
//     here, not silently succeeds.
//   - svc.Can approves: elevated — ActorID stays the originally-blocked
//     session user (for InsertAuditElevated's blockedActorID), ApproverID
//     is who actually gets to act.
func checkOrElevate(dp *common.Deps, r *http.Request, action, pin string) elevationCheck {
	actorID := getSessionUserID(r)

	if canPerform(dp, r, action) {
		return elevationCheck{Outcome: allowed, ActorID: actorID}
	}
	if pin == "" {
		return elevationCheck{Outcome: needsElevation, ActorID: actorID}
	}

	svc := dp.AuthSvc
	if svc == nil {
		// Fail closed rather than silently minting a fresh auth.Service
		// (see errAuthServiceUnavailable's doc comment) — deny instead of
		// risking a lockout-counter reset if this ever becomes reachable.
		return elevationCheck{Outcome: needsElevation, ActorID: actorID, Err: errAuthServiceUnavailable}
	}

	// Shares the login lockout (checkPIN), so this is no more of a
	// brute-force oracle than the manager-PIN gates it generalizes.
	approver, err := svc.AuthorizeManager(r.Context(), pin)
	if err != nil {
		return elevationCheck{Outcome: needsElevation, ActorID: actorID, Err: err}
	}

	// AuthorizeManager only confirmed "some manager/admin/super_admin PIN" —
	// re-check the APPROVER (not the session user canPerform would read)
	// actually holds THIS action, exactly as fiscal_api.go re-checks the
	// PIN owner's role before trusting it.
	can, err := svc.Can(r.Context(), approver, action)
	if err != nil {
		return elevationCheck{Outcome: needsElevation, ActorID: actorID, Err: err}
	}
	if !can {
		return elevationCheck{Outcome: needsElevation, ActorID: actorID, Err: errApproverInsufficientRole}
	}

	return elevationCheck{Outcome: elevated, ActorID: actorID, ApproverID: approver.ID}
}

// elevationErrorKey maps checkOrElevate's Err into an i18n key for the
// elevation prompt's inline error span — nil (no attempt yet / first-time
// prompt) renders no error at all.
func elevationErrorKey(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, auth.ErrLockedOut):
		return "elevation.error_locked_out"
	case errors.Is(err, auth.ErrInvalidPIN):
		return "elevation.error_invalid_pin"
	case errors.Is(err, errAuthServiceUnavailable):
		return "elevation.error_unavailable"
	default: // errApproverInsufficientRole, or any other AuthorizeManager/Can failure
		return "elevation.error_insufficient_role"
	}
}

// elevationHiddenField mirrors one field of the original triggering form —
// the elevation dialog's own form re-POSTs to the SAME URL/method as the
// original action, so anything besides override_pin the handler needs must
// round-trip through here.
type elevationHiddenField struct{ Name, Value string }

// renderElevationPrompt writes the elevation dialog fragment (web/ui/
// partials/elevation_prompt.html) as the response — status 200. The
// response body carries two independently-swapped pieces (see that
// template's own doc comment): a small hint that lands in hxTarget (the
// SAME target the triggering element already uses, so a denial still
// renders something in place instead of a flat 403), and the dialog itself,
// out-of-band-swapped into the page's own top-level #elevation-modal
// placeholder (web/ui/layouts/base.html) rather than nested inside hxTarget
// — hxTarget is still passed through as the DIALOG's own retry target
// (postAction is the original handler's own URL; the dialog's form re-POSTs
// there, and its response — success OR another prompt — must land back in
// hxTarget exactly as the original request would have). hidden replays
// whatever else that form needs beyond override_pin. summary is a
// human-readable, pre-translated description of the specific action being
// approved (Fix for ut-docs#557's "approver can't see what they're
// approving" finding) — empty renders no summary line.
func renderElevationPrompt(w http.ResponseWriter, r *http.Request, postAction, hxTarget, summary string, hidden []elevationHiddenField, check elevationCheck) {
	httpx.RenderPartial("ui/partials/elevation_prompt.html", map[string]any{
		"Action":   postAction,
		"HxTarget": hxTarget,
		"Hidden":   hidden,
		"Summary":  summary,
		"ErrKey":   elevationErrorKey(check.Err),
	})(w, r)
}
