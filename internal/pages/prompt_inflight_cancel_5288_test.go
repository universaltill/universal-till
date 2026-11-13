package pages

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#5288: while a prompt's own request is in flight, Cancel and Escape
// are inert (disabled / ignored) -- never an abort: htmx:abort is client-side
// only and the retry is not idempotent, so closing mid-request could leave the
// action done with no answer shown. The e2e specs prove the behaviour; these
// pin the markup the behaviour hangs on.

var promptFormOpen = regexp.MustCompile(`<form hx-post="[^"]*"[^>]*>`)

// requireCancelDisabledInFlight: the form's hx-disabled-elt must cover every
// button (so Cancel as well as submit) except the PIN pad's keyboard toggle.
// (htmx 1.9's "find" returns only the FIRST match and a comma list is not
// split, so the selector is a plain document-wide one scoped by the dialog id.)
func requireCancelDisabledInFlight(t *testing.T, body, id string) {
	t.Helper()
	m := promptFormOpen.FindString(body)
	if m == "" {
		t.Fatalf("no prompt <form hx-post> in: %s", body)
	}
	if !strings.Contains(m, `hx-disabled-elt="#`+id+` button:not([data-osk-toggle])"`) {
		t.Fatalf("prompt form must disable Cancel too while in flight (hx-disabled-elt covering every button but the OSK toggle), got: %s", m)
	}
	if !strings.Contains(body, `data-action="close-closest"`) {
		t.Fatalf("prompt has no Cancel (data-action=close-closest) for hx-disabled-elt to cover: %s", body)
	}
}

func TestElevationPrompt_CancelDisabledWhileInFlight(t *testing.T) {
	dp := newElevationTestDeps(t)
	r := elevationRequest(auth.User{ID: "cash-session", Role: "cashier"})
	check := checkOrElevate(dp, r, "sync_management", "")
	rec := httptest.NewRecorder()
	renderElevationPrompt(rec, r, "/api/sync/promote", "#promote-msg", "Promote this till.", nil, check)
	body := rec.Body.String()
	requireCancelDisabledInFlight(t, body, "elevation-modal")
	if !strings.Contains(body, "data-osk-toggle") {
		t.Fatal("the PIN dialog lost its OSK toggle, which the selector above must keep enabled")
	}
}

func TestConfirmPrompt_CancelDisabledWhileInFlight(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x", nil)
	rec := httptest.NewRecorder()
	renderConfirmPrompt(rec, r, confirmPrompt{Action: "/api/x", HxTarget: "#x-msg", Title: "Sure?", Body: "Really.", Confirm: "Yes"})
	requireCancelDisabledInFlight(t, rec.Body.String(), "confirm-modal")
}

// Escape (ut-motion.js) and close-closest / close (inline-actions.js) both refuse
// to close a dialog whose own form has an htmx request in flight.
func TestEscapeAndCloseRefuseWhileFormInFlight_5288(t *testing.T) {
	base, err := os.ReadFile("web/public/ut-motion.js") // TestMain chdirs to the repo root; the Escape handler moved here (ut-docs#2972)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(base), "top.hasAttribute('data-ut-escape-close')")
	if i < 0 {
		t.Fatal("ut-motion.js's Escape handler changed shape; update this test")
	}
	handler := string(base)[i:min(len(base), i+600)]
	if !strings.Contains(handler, "top.hasAttribute('data-ut-prompt') && top.querySelector('form.htmx-request')") {
		t.Fatalf("ut-motion.js's Escape handler must ignore Escape while the dialog's own form is in flight (form.htmx-request), got: %s", handler)
	}
	ia, err := os.ReadFile("web/public/inline-actions.js")
	if err != nil {
		t.Fatal(err)
	}
	cd := regexp.MustCompile(`function closeDialog\(d\) \{[^\n]*\}`).FindString(string(ia))
	// Scoped to prompts (review M1): an unscoped guard stranded the self-order
	// kiosk's close-clear Cancel behind an emptied, still-open modal.
	if !strings.Contains(cd, "htmx-request") || !strings.Contains(cd, "data-ut-prompt") {
		t.Fatalf("inline-actions.js closeDialog must refuse only a prompt (data-ut-prompt) whose form is in flight, got: %q", cd)
	}
}
