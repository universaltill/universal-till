package recovery

import (
	"context"
	"database/sql"
	"encoding/csv"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
)

// probeSafeMode reports whether ListSalesJournal actually succeeds against
// safeModeDB, right now — NOT whether the connection merely opened.
// Independent review (ut-docs#1436) found a real gap: db.OpenReadOnly
// succeeding only proves the SQLite file itself is readable, not that its
// schema is new enough for this query. ListSalesJournal joins `tills`
// (migration 014) and reads `sales.till_id` (015) — a migration failure
// anywhere before those leaves a perfectly openable database this query
// still can't run against. Called once, at Serve time, to decide whether to
// advertise safe mode at all — the alternative (advertising it and letting
// each request 500) is exactly what independent review caught live.
func probeSafeMode(safeModeDB *sql.DB) bool {
	_, _, err := data.NewPOSRepo(safeModeDB).ListSalesJournal(context.Background(), data.SalesJournalFilter{
		AllTills: true,
		Day:      time.Now().Format("2006-01-02"),
		Limit:    1,
	})
	return err == nil
}

// csvSafe defuses CSV/formula injection, same convention and reasoning as
// internal/pages/csv_export.go's own csvSafe (ut-docs#195/#1020) — kept as a
// small local duplicate rather than exported cross-package, since unifying
// the two would mean touching that file's 5 existing call sites for a
// one-function need; independent review flagged the gap and named this as
// an accepted option. ReceiptNo embeds the shop's freely-editable
// sync.receipt_prefix setting (see invoice_page.go's own comment on this
// exact field); TillName is an operator-typed enrolled-till name. Both are
// attacker-reachable the same way csv_export.go's originals are.
func csvSafe(field string) string {
	if field == "" || field == "-" {
		return field
	}
	switch field[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + field
	default:
		return field
	}
}

// requireLoopback refuses a request whose remote address isn't the local
// machine (ut-docs#1436 independent review): cfg.ListenAddr defaults to
// ":8080" — every interface, the whole shop LAN — and unlike normal
// operation's /journal (behind auth.Middleware), recovery mode has no
// session store to authenticate against (the database that just failed may
// be exactly what auth would need to read). Every legitimate caller already
// connects via 127.0.0.1: cmd/unitill-desktop, MainActivity's in-process
// TillService, and the Pi kiosk launcher (UT_KIOSK_URL defaults to
// http://127.0.0.1:8080) all do. This is the smallest-privilege gate that
// doesn't need real authentication infrastructure recovery mode has no safe
// way to build against a possibly-broken database.
func requireLoopback(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "safe mode is only reachable from this device", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// registerSafeModeRoutes wires the read-only "see/export today's sales"
// routes (ut-docs#1436 AC: "Start in safe mode where possible ... open the
// database read-only at the last applied migration version so the operator
// can at least see/print today's sales and export data (no writes)").
// safeModeDB must already be a read-only connection (db.OpenReadOnly) — this
// package never opens anything for writing. Only called once probeSafeMode
// has already confirmed the query works against this database (see Serve).
func registerSafeModeRoutes(mux *http.ServeMux, safeModeDB *sql.DB) {
	log := logging.L()
	repo := data.NewPOSRepo(safeModeDB)

	list := func(r *http.Request) ([]data.SaleJournalEntry, error) {
		today := time.Now().Format("2006-01-02")
		entries, _, err := repo.ListSalesJournal(r.Context(), data.SalesJournalFilter{
			AllTills: true,
			Day:      today,
			Limit:    1000,
		})
		return entries, err
	}

	mux.HandleFunc("GET /recovery/safe-mode", requireLoopback(func(w http.ResponseWriter, r *http.Request) {
		entries, err := list(r)
		if err != nil {
			// The raw driver error isn't shown to the client — it already
			// passed probeSafeMode, so a later failure here is unexpected
			// and logged for whoever investigates, not echoed over HTTP.
			log.Errorf("recovery mode: safe-mode list sales: %v", err)
			http.Error(w, "safe mode is temporarily unavailable", http.StatusInternalServerError)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		tpl, err := template.New("safemode.html").Funcs(httpx.FuncsFor(locale)).ParseFS(templatesFS, "templates/safemode.html")
		if err != nil {
			log.Errorf("recovery mode: parse safe-mode template: %v", err)
			http.Error(w, "safe mode is temporarily unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tpl.ExecuteTemplate(w, "safemode.html", map[string]any{"Entries": entries}); err != nil {
			log.Errorf("recovery mode: render safe-mode template: %v", err)
		}
	}))

	mux.HandleFunc("GET /recovery/safe-mode/export.csv", requireLoopback(func(w http.ResponseWriter, r *http.Request) {
		entries, err := list(r)
		if err != nil {
			log.Errorf("recovery mode: safe-mode export: %v", err)
			http.Error(w, "safe mode is temporarily unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="sales-today-safe-mode.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"receipt_no", "total_minor_units", "tender_type", "created_at", "till_name"})
		for _, e := range entries {
			_ = cw.Write([]string{csvSafe(e.ReceiptNo), strconv.FormatInt(e.Total, 10), e.TenderType, e.CreatedAt, csvSafe(e.TillName)})
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			log.Errorf("recovery mode: flush safe-mode CSV: %v", err)
		}
	}))
}
