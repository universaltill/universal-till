package common

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
)

// LocalizedError writes an http.Error response translated for the request's
// resolved locale (query param, then cookie, then default — the same
// resolution template rendering uses), in place of a raw
// http.Error(w, "some English literal", status) call. Use this for any
// operator-facing error text; see ut-docs#316 — the guard that would
// normally catch a hardcoded string here (guard-i18n.sh) deliberately
// exempts http.Error bodies, so nothing else stops one from creeping back
// in.
func LocalizedError(w http.ResponseWriter, r *http.Request, status int, key string) {
	http.Error(w, httpx.T(httpx.ResolveLocale(w, r), key), status)
}

// LogAndLocalizedError is LocalizedError's counterpart for a raw error that
// must never reach the operator's screen — a Go/SQL error, which can carry
// an internal ID or other detail no operator should see (ut-docs#316, the
// same defect class ut-docs#303 fixed for barcode conflicts specifically
// via FriendlyBarcodeConflict below). It logs the real error server-side,
// prefixed with logTag for grepability, and shows the operator only the
// translated key.
//
// Logging goes through the app's own leveled logger (internal/logging),
// not stdlib log, so every call site (101 across internal/pages) gets the
// same structured [LEVEL] formatting as the rest of the app's logging, and
// — since this logs at Error level — also flows into logging.Recent(),
// the in-memory Problems ring the cloud-sync heartbeat reports (ADR-0018).
// Before this, an error routed through this helper was invisible to that
// feed even though it's exactly the kind of server-side problem it exists
// to surface (ut-docs#947).
func LogAndLocalizedError(w http.ResponseWriter, r *http.Request, status int, key string, logTag string, err error) {
	logging.L().Errorf("[%s] %v", logTag, err)
	LocalizedError(w, r, status, key)
}
