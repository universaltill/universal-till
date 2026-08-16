package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// newElevationTestDeps mirrors newPermissionSettingsTestDeps' setup
// (permission_settings_page_test.go) plus two PIN-bearing operators —
// a manager and a cashier — for checkOrElevate's AuthorizeManager leg.
func newElevationTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	authRepo := data.NewAuthRepo(db)
	ctx := t.Context()

	mgrID, err := authRepo.CreateUser(ctx, "mgr1", "Manager One", "manager")
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	mgrHash, err := auth.HashPIN("135790")
	if err != nil {
		t.Fatalf("hash manager pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, mgrID, mgrHash); err != nil {
		t.Fatalf("set manager pin: %v", err)
	}

	cashID, err := authRepo.CreateUser(ctx, "cash1", "Cashier One", "cashier")
	if err != nil {
		t.Fatalf("create cashier: %v", err)
	}
	cashHash, err := auth.HashPIN("246800")
	if err != nil {
		t.Fatalf("hash cashier pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, cashID, cashHash); err != nil {
		t.Fatalf("set cashier pin: %v", err)
	}

	return &common.Deps{Cfg: &config.Config{}, Db: db, AuthSvc: auth.NewService(db)}
}

func elevationRequest(u auth.User) *http.Request {
	return auth.WithUser(httptest.NewRequest(http.MethodPost, "/elevation-test", nil), u)
}

// A session that can already perform the action needs no elevation at all.
func TestCheckOrElevate_Allowed(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "mgr-session", Role: "manager"})

	got := checkOrElevate(dp, r, "settings", "")

	if got.Outcome != allowed {
		t.Fatalf("Outcome = %v, want allowed", got.Outcome)
	}
	if got.ActorID != "mgr-session" {
		t.Fatalf("ActorID = %q, want mgr-session", got.ActorID)
	}
	if got.ApproverID != "" {
		t.Fatalf("ApproverID = %q, want empty on the allowed path", got.ApproverID)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
}

// A denied session with no PIN supplied is a first-time prompt, not an error.
func TestCheckOrElevate_NeedsElevation_NoPIN(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})

	got := checkOrElevate(dp, r, "settings", "")

	if got.Outcome != needsElevation {
		t.Fatalf("Outcome = %v, want needsElevation", got.Outcome)
	}
	if got.ActorID != "cash-session" {
		t.Fatalf("ActorID = %q, want cash-session", got.ActorID)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil (first-time prompt, not a failed attempt)", got.Err)
	}
}

// A wrong PIN is a real failed attempt: needsElevation with Err set, so the
// caller can render an actual error instead of a first-time prompt.
func TestCheckOrElevate_NeedsElevation_WrongPIN(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})

	got := checkOrElevate(dp, r, "settings", "000000")

	if got.Outcome != needsElevation {
		t.Fatalf("Outcome = %v, want needsElevation", got.Outcome)
	}
	if got.ActorID != "cash-session" {
		t.Fatalf("ActorID = %q, want cash-session", got.ActorID)
	}
	if got.Err == nil {
		t.Fatal("Err = nil, want a wrong-PIN error")
	}
}

// A PIN that authenticates fine (manager role) but whose role still can't
// perform THIS action (permission_management is super_admin-only) must not
// silently succeed as though the approver could do it.
func TestCheckOrElevate_NeedsElevation_ApproverInsufficientRole(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})

	got := checkOrElevate(dp, r, "permission_management", "135790")

	if got.Outcome != needsElevation {
		t.Fatalf("Outcome = %v, want needsElevation", got.Outcome)
	}
	if got.ApproverID != "" {
		t.Fatalf("ApproverID = %q, want empty — the manager must not be treated as an approver here", got.ApproverID)
	}
	if got.Err == nil {
		t.Fatal("Err = nil, want an insufficient-role error")
	}
}

// A correct approver PIN whose role CAN perform the action elevates: the
// action proceeds as the approver, and ActorID still carries the originally
// blocked session user for dual attribution.
func TestCheckOrElevate_Elevated(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})

	got := checkOrElevate(dp, r, "settings", "135790")

	if got.Outcome != elevated {
		t.Fatalf("Outcome = %v, want elevated", got.Outcome)
	}
	if got.ActorID != "cash-session" {
		t.Fatalf("ActorID = %q, want cash-session (the originally-blocked user)", got.ActorID)
	}
	if got.Err != nil {
		t.Fatalf("Err = %v, want nil", got.Err)
	}
	// ApproverID is the manager's real id, resolved via the PIN — assert it
	// resolves to a real, active manager rather than pinning the seeded id
	// literal (which newElevationTestDeps' CreateUser call already owns).
	if got.ApproverID == "" || got.ApproverID == got.ActorID {
		t.Fatalf("ApproverID = %q, want the manager's own id", got.ApproverID)
	}
}

// Auth disabled (UT_AUTH=off) short-circuits canPerform to true, same escape
// hatch every other canPerform() call site gets — no elevation needed.
func TestCheckOrElevate_AuthDisabled_Allowed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newElevationTestDeps(t)
	r := httptest.NewRequest(http.MethodPost, "/elevation-test", nil) // no session at all

	got := checkOrElevate(dp, r, "settings", "")

	if got.Outcome != allowed {
		t.Fatalf("Outcome = %v, want allowed", got.Outcome)
	}
}

// --- ut-docs#557 review Fix 1: the elevation dialog's own <form> must
// never end up nested inside the triggering action's outer <form> ---
//
// Rendering the dialog straight into the SAME hx-target the triggering
// element's own response uses (e.g. tills.html's #promote-msg, itself
// inside <form hx-post="/api/sync/promote">) used to nest the dialog's own
// <form> (PIN input, hidden fields, submit button) inside that outer form.
// Per the HTML tree-construction algorithm, a <form> start tag is ignored
// outright while a form element pointer is already open — parsing "outer
// form > target > dialog > inner form" as one continuous document loses the
// inner form entirely. The fix renders the dialog out-of-band
// (hx-swap-oob="true") into its own top-level #elevation-modal placeholder
// (web/ui/layouts/base.html) instead of nesting it in hxTarget at all.
// golang.org/x/net/html implements the same WHATWG tree-construction
// algorithm a browser's parser runs, so it's used here (not a browser) to
// pin the structural claim down precisely.

// findAllTag returns every descendant of n (n included) with the given tag.
func findAllTag(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == tag {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// hasAttr reports whether n carries an attribute with the given key.
func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// hasDescendantInput reports whether n has a descendant <input name=name>.
func hasDescendantInput(n *html.Node, name string) bool {
	for _, in := range findAllTag(n, "input") {
		for _, a := range in.Attr {
			if a.Key == "name" && a.Val == name {
				return true
			}
		}
	}
	return false
}

// isDescendantOfTag reports whether n has an ancestor element with the
// given tag (n itself doesn't count).
func isDescendantOfTag(n *html.Node, tag string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag {
			return true
		}
	}
	return false
}

// TestElevationPrompt_OldNestedRenderLosesInnerForm pins down the BUG Fix 1
// corrects: the pre-fix shape (the dialog's <form> rendered straight inside
// the triggering hx-target, itself inside the page's own outer <form>)
// loses its inner <form> to the tree-construction algorithm's "form element
// pointer already open" rule the moment it's parsed as one document.
func TestElevationPrompt_OldNestedRenderLosesInnerForm(t *testing.T) {
	oldShape := `<!DOCTYPE html><html><body>` +
		`<form hx-post="/api/sync/promote" id="outer-form">` +
		`<div id="promote-msg">` +
		`<dialog class="modifier-modal elevation-dialog">` +
		`<form hx-post="/api/sync/promote" hx-target="#promote-msg">` +
		`<input type="password" name="override_pin">` +
		`<button type="submit">Confirm</button>` +
		`</form></dialog>` +
		`</div></form></body></html>`

	doc, err := html.Parse(strings.NewReader(oldShape))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	forms := findAllTag(doc, "form")
	if len(forms) != 1 {
		t.Fatalf("pre-fix shape: expected the inner <form> to be dropped by "+
			"the tree-construction algorithm (1 surviving <form>), got %d", len(forms))
	}
	// The PIN input still parses — just with no <form> wrapper of its own,
	// which is exactly the defect: a dialog that LOOKS like it carries its
	// own form but structurally does not, so it can't submit independently
	// of the outer one.
	if !hasDescendantInput(doc, "override_pin") {
		t.Fatal("expected the override_pin input to still be present (just unwrapped)")
	}
}

// TestElevationPrompt_OOBRenderKeepsInnerFormUnnested exercises the ACTUAL
// fix through the real production code path: renderElevationPrompt's real
// output, assembled into the real page shape (the triggering hx-target
// inside its own outer <form> — tills.html's real shape — and the
// #elevation-modal placeholder OUTSIDE that form — base.html's real shape),
// keeps the dialog's own <form> a real, non-nested element. The OOB piece
// is moved to the placeholder's position before parsing — the exact end
// state hx-swap-oob="true"'s default outerHTML swap produces in a browser.
func TestElevationPrompt_OOBRenderKeepsInnerFormUnnested(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})
	check := checkOrElevate(dp, r, "sync_management", "")

	rec := httptest.NewRecorder()
	renderElevationPrompt(rec, r, "/api/sync/promote", "#promote-msg",
		"Promote this till to a standalone primary.", nil, check)
	body := rec.Body.String()
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("expected the dialog fragment to carry hx-swap-oob=\"true\", got: %s", body)
	}

	bodyCtx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	frag, err := html.ParseFragment(strings.NewReader(body), bodyCtx)
	if err != nil {
		t.Fatalf("parse fragment: %v", err)
	}
	var mainHTML, oobHTML strings.Builder
	for _, n := range frag {
		if n.Type == html.ElementNode && hasAttr(n, "hx-swap-oob") {
			_ = html.Render(&oobHTML, n)
		} else {
			_ = html.Render(&mainHTML, n)
		}
	}
	if oobHTML.Len() == 0 {
		t.Fatal("expected an OOB-tagged node (the dialog) in the response")
	}

	page := `<!DOCTYPE html><html><body>` +
		`<form hx-post="/api/sync/promote" id="outer-form">` +
		`<div id="promote-msg">` + mainHTML.String() + `</div>` +
		`</form>` +
		oobHTML.String() +
		`</body></html>`

	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatalf("parse assembled page: %v", err)
	}
	forms := findAllTag(doc, "form")
	if len(forms) != 2 {
		t.Fatalf("expected 2 real <form> elements (outer + the dialog's own), got %d:\n%s", len(forms), page)
	}
	var pinForm *html.Node
	for _, f := range forms {
		if hasDescendantInput(f, "override_pin") {
			pinForm = f
		}
	}
	if pinForm == nil {
		t.Fatalf("no <form> contains the override_pin input:\n%s", page)
	}
	if isDescendantOfTag(pinForm, "form") {
		t.Fatal("the elevation dialog's own <form> must NOT be nested inside another <form>")
	}
}

// ut-docs#557 review Fix 5b: a nil dp.AuthSvc must fail CLOSED (needsElevation
// with an error) rather than silently minting a fresh auth.Service, which
// would reset the shared-device PIN lockout counter if this path ever
// becomes reachable in practice (today it isn't — canPerform() itself would
// nil-panic on dp.AuthSvc first).
func TestCheckOrElevate_NilAuthSvc_FailsClosed(t *testing.T) {
	dp := newElevationTestDeps(t)
	dp.AuthSvc = nil
	// UT_AUTH left at its default (auth ON): canPerform()'s own auth.
	// FromContext lookup denies a request carrying no session WITHOUT ever
	// touching d.AuthSvc (authz.go), so this reaches checkOrElevate's
	// svc-nil branch safely — a real PIN is supplied, so it's genuinely
	// exercising "pin set, svc nil", not the earlier pin=="" branch.
	r := httptest.NewRequest(http.MethodPost, "/elevation-test", nil) // no session → canPerform denies, no panic

	got := checkOrElevate(dp, r, "settings", "000000")

	if got.Outcome != needsElevation {
		t.Fatalf("Outcome = %v, want needsElevation", got.Outcome)
	}
	if got.Err == nil {
		t.Fatal("Err = nil, want an error — a nil AuthSvc must fail closed, not silently mint a fresh auth.Service")
	}
}
