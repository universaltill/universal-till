package pages

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#3212: a .show() dialog has no keyboard trap of its own -- the
// shared #ut-scrim blocks the pointer only -- so base.html traps Tab inside
// the topmost dialog that opts in with data-ut-focus-trap (or
// data-ut-escape-close, which implies it). The elevation and confirm
// dialogs are OOB-swapped over base.html's placeholders, so the swapped-in
// element is the one whose attributes count: both the rendered fragment
// and the placeholder must carry the opt-in.

var focusTrapDialogOpen = regexp.MustCompile(`<dialog id="(elevation-modal|confirm-modal)"[^>]*>`)

func requireFocusTrap(t *testing.T, body, id string) {
	t.Helper()
	for _, m := range focusTrapDialogOpen.FindAllStringSubmatch(body, -1) {
		if m[1] != id {
			continue
		}
		if !strings.Contains(m[0], "data-ut-focus-trap") {
			t.Fatalf("#%s must opt into base.html's focus trap (data-ut-focus-trap), got: %s", id, m[0])
		}
		return
	}
	t.Fatalf("no <dialog id=%q> in: %s", id, body)
}

func TestElevationPrompt_DialogOptsIntoFocusTrap(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})
	check := checkOrElevate(dp, r, "sync_management", "")
	rec := httptest.NewRecorder()
	renderElevationPrompt(rec, r, "/api/sync/promote", "#promote-msg", "Promote this till.", nil, check)
	requireFocusTrap(t, rec.Body.String(), "elevation-modal")
}

func TestConfirmPrompt_DialogOptsIntoFocusTrap(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x", nil)
	rec := httptest.NewRecorder()
	renderConfirmPrompt(rec, r, confirmPrompt{Action: "/api/x", HxTarget: "#x-msg", Title: "Sure?", Body: "Really.", Confirm: "Yes"})
	requireFocusTrap(t, rec.Body.String(), "confirm-modal")
}

func TestBaseLayout_DialogPlaceholdersOptIntoFocusTrap(t *testing.T) {
	b, err := os.ReadFile("web/ui/layouts/base.html") // TestMain chdirs to the repo root
	if err != nil {
		t.Fatal(err)
	}
	requireFocusTrap(t, string(b), "elevation-modal")
	requireFocusTrap(t, string(b), "confirm-modal")
}
