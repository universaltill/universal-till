package httpx

import "net/http"

// RefuseText answers a refused request with msg as plain text, like
// http.Error, and marks it X-UT-Response: refused — "this body is an
// already-translated reason meant for the operator". inline-actions.js's
// save-error step (ut-docs#2982) shows a marked body next to the card
// that made the request; anything unmarked (an untranslated developer
// string such as "could not save") keeps the page's generic banner, so
// only pass msg through T. The X-UT-Response vocabulary is shared with
// the 200 "refused" fragments (users_page.go, display-mode); this is its
// non-2xx, text/plain form.
func RefuseText(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("X-UT-Response", "refused")
	http.Error(w, msg, status)
}
