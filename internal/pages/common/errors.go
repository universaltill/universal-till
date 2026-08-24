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
// not stdlib log, so every call site (101+ across internal/pages) gets the
// same structured [LEVEL] formatting as the rest of the app's logging.
// The level is derived from status: a 5xx logs at Error and flows into
// logging.Recent(), the in-memory Problems ring the cloud-sync heartbeat
// reports (ADR-0018) — the behavior #947 shipped. A 4xx logs at Info
// instead — still logged and grepable server-side via logTag, but kept
// out of Recent() entirely, since it's routinely operator-triggerable (a
// malformed form, a declined tender) rather than a real server problem.
// At the default UT_LOG_LEVEL (info) this still lands on stdout,
// grepable by logTag; a shop that raises UT_LOG_LEVEL to warn/error will
// no longer see 4xx lines at all — the same filtering that already
// applies to any other Info-level line, not special-cased here.
// Before this, every call landed at Error regardless of status, so a
// cashier fat-fingering a form repeatedly could evict a genuine warning
// from the ring's/digest's limited, uncapped-by-priority slots
// (ut-docs#954, follow-up to #947).
func LogAndLocalizedError(w http.ResponseWriter, r *http.Request, status int, key string, logTag string, err error) {
	if status >= http.StatusInternalServerError {
		logging.L().Errorf("[%s] %v", logTag, err)
	} else {
		logging.L().Infof("[%s] %v", logTag, err)
	}
	LocalizedError(w, r, status, key)
}
