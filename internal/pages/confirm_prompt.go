package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
)

// confirmPrompt is a plain "are you sure?" dialog (ut-docs#2781) — the
// elevation prompt's shape (elevation.go's renderElevationPrompt) without
// the PIN: the dialog's own form re-POSTs Action with Hidden plus whatever
// confirming field the caller put in Hidden, and its response lands in
// HxTarget exactly as the original request's would have. Title, Body and
// Confirm are pre-translated; HelpTopic, when set, puts that manual
// topic's "?" beside the title.
type confirmPrompt struct {
	Action    string
	HxTarget  string
	Title     string
	Body      string
	Confirm   string
	HelpTopic string
	Hidden    []elevationHiddenField
}

// renderConfirmPrompt writes the confirm dialog fragment (web/ui/partials/
// confirm_prompt.html), status 200: a small hint for HxTarget plus the
// dialog itself, OOB-swapped into base.html's #confirm-modal placeholder
// and opened with .show() over #ut-scrim — never showModal(), same reason
// as the elevation dialog. X-UT-Response "confirm-prompt" tells it apart
// from a real answer (the elevation prompt's "elevation-prompt" pattern);
// text/html keeps a settings form's `not-html` guard from navigating away
// over it.
func renderConfirmPrompt(w http.ResponseWriter, r *http.Request, p confirmPrompt) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-UT-Response", "confirm-prompt")
	httpx.RenderPartial("ui/partials/confirm_prompt.html", map[string]any{
		"Action":    p.Action,
		"HxTarget":  p.HxTarget,
		"Title":     p.Title,
		"Body":      p.Body,
		"Confirm":   p.Confirm,
		"HelpTopic": p.HelpTopic,
		"Hidden":    p.Hidden,
	})(w, r)
}
