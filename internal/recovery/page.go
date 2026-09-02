package recovery

import (
	"embed"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
)

//go:embed templates/recovery.html templates/safemode.html
var templatesFS embed.FS

// messageKey maps a Kind to the locale key for its plain-language
// explanation — kept separate from Kind itself so a new Kind can't be added
// without also deciding what an operator reads (falls back to
// recovery.unknown_error, which every locale file still must define).
func messageKey(k Kind) string {
	switch k {
	case KindMigration:
		return "recovery.migration_failed"
	case KindDBOpen:
		return "recovery.db_open_failed"
	case KindDiskFull:
		return "recovery.disk_full"
	default:
		return "recovery.unknown_error"
	}
}

// pageHandler renders the top-level recovery page. safeModeAvailable is
// decided once per Serve call (whether a read-only DB handle could be
// opened), not per-request — recovery mode's DB state doesn't change
// between requests within one Serve lifetime.
func pageHandler(failure Failure, safeModeAvailable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		tpl, err := template.New("recovery.html").Funcs(httpx.FuncsFor(locale)).ParseFS(templatesFS, "templates/recovery.html")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data := map[string]any{
			"Message":           httpx.T(locale, messageKey(failure.Kind)),
			"Detail":            failure.Detail,
			"RefCode":           failure.RefCode,
			"SafeModeAvailable": safeModeAvailable,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tpl.ExecuteTemplate(w, "recovery.html", data); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

// minRetryInterval throttles POST /api/recovery/retry (independent review,
// ut-docs#1436): the endpoint is unauthenticated by necessity (see
// requireLoopback's doc comment on why recovery mode has no session store
// to authenticate against), and each retry re-runs real file-system work
// (legacy-data migration check, pending-restore apply, a fresh db.Open) —
// this bounds how often that can be driven, from any source, including a
// well-meaning operator's double-click.
const minRetryInterval = 2 * time.Second

// retryHandler signals retry (non-blocking, buffered channel — a second
// click while a retry is already pending is a harmless no-op) and responds
// immediately; the actual re-attempt happens in app.Run's boot loop once
// Serve returns, not inside this handler. lastRetry is closed over by the
// single mux registration in Serve, so it's naturally scoped to one boot
// attempt — never persisted or shared across Serve calls.
func retryHandler(retry chan<- struct{}) http.HandlerFunc {
	var mu sync.Mutex
	var last time.Time
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tooSoon := !last.IsZero() && time.Since(last) < minRetryInterval
		if !tooSoon {
			last = time.Now()
		}
		mu.Unlock()
		if tooSoon {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		select {
		case retry <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Recovery mode is, by definition, not healthy — every shell's
		// "don't lock the kiosk / don't consider the till usable until
		// healthy" logic (ut-docs#1437, #1438) keys off this staying
		// non-200 for the entire time recovery mode is serving.
		http.Error(w, "recovery mode", http.StatusServiceUnavailable)
	}
}
