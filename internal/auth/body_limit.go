package auth

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// ExemptBodyMax caps the body of any request the session wall lets through
// unauthenticated (exempt / exemptRequest). Those routes are reachable from
// the LAN (ADR-0157), so a peer needs no credential to make the till read
// their body; the default is sized for the small forms and JSON they take
// (ut-docs#5338). A new exempt route inherits it without anyone remembering
// to add a cap.
const ExemptBodyMax = 64 << 10

// exemptBodyOverrides lists exempt routes whose real payload exceeds the
// default. Each must be a registered POST route
// (pages.TestExemptBodyLimitOverridesAreRoutes) and be at least the handler's
// own cap (pages.TestHandlerCapsFitExemptBodyLimit). The /api/sync/sales
// figure is pinned by pages.TestMaxSyncSalesBatchFitsExemptLimit: a replica
// that is refused a batch never advances its cursor, so it must not be
// possible for a legitimate batch to hit the cap.
var exemptBodyOverrides = map[string]int64{
	"/api/sync/sales":             64 << 20,  // <=100 journal sales; wedge guard
	"/api/sync/closes":            4 << 20,   // = pages.salesMirrorClosesMaxBody
	"/api/sync/held-sales/upsert": 4 << 20,   // one parked basket payload
	"/api/sync/catalog/apply":     1 << 20,   // = pages.maxSyncCatalogBody
	"/api/sync/settings/apply":    256 << 10, // = pages.maxSyncSettingsBody
	"/api/sync/no-sales":          256 << 10, // <=100 rows
	"/api/sync/cleared-baskets":   256 << 10, // <=100 rows
}

// ExemptBodyLimit is the body cap Middleware applies to an exempt path.
func ExemptBodyLimit(path string) int64 {
	if n, ok := exemptBodyOverrides[path]; ok {
		return n
	}
	return ExemptBodyMax
}

// ExemptBodyOverridePaths returns the overridden paths, sorted.
func ExemptBodyOverridePaths() []string {
	out := make([]string, 0, len(exemptBodyOverrides))
	for p := range exemptBodyOverrides {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// limitExemptBody enforces ExemptBodyLimit: a declared oversize body is
// refused with 413 before the handler runs; an undeclared (chunked) one is
// wrapped so the handler's read fails with *http.MaxBytesError. It reports
// false when it has already answered.
func limitExemptBody(w http.ResponseWriter, r *http.Request) bool {
	if r.ContentLength == 0 || r.Body == nil || r.Body == http.NoBody {
		return true
	}
	limit := ExemptBodyLimit(r.URL.Path)
	if r.ContentLength > limit {
		writeBodyTooLarge(w, r)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

// writeBodyTooLarge answers 413 and closes the connection (the unread body
// is not worth draining). Machine-facing, so English like the 401 contract.
func writeBodyTooLarge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Connection", "close")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": nil, "error": map[string]string{"code": "body_too_large", "message": "request body too large"},
		})
		return
	}
	http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
}
