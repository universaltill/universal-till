package pages

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// maxExportRangeDays bounds POST /api/data/export's [from, to] span
// (ut-docs#229). 366 covers a full calendar year (including a leap Feb)
// end-to-end plus one day of slack, which is generous for the stated
// shop-month-end-export use case while still ruling out an accidentally
// (or maliciously) unbounded request.
const maxExportRangeDays = 366

const maxExportRange = maxExportRangeDays * 24 * time.Hour

// maxExportSalesRows bounds how many matched sales POST /api/data/export
// will gather and marshal into the export/report plugin's stdin (ut-docs#439,
// follow-up to #229's range cap). The range cap alone only bounds elapsed
// *time* -- 366 days of a busy till's sales can still be six-figure rows
// loaded into one in-memory slice on Pi-class hardware, independent of how
// short the query itself runs. 50,000 rows is generous for the stated
// month/quarter/year-end export use case (a very busy quick-service till
// doing ~135 sales/day, every day, for a year) while still ruling out the
// six-figure case the range cap alone can't catch; a shop that genuinely
// exceeds it can narrow the date range and export in parts. A `var`, not a
// `const`, so tests can override it rather than seeding 50,000 rows.
var maxExportSalesRows = 50_000

// exportRequestPayload is the event payload a subscribing export/report
// plugin receives on "export.requested.ask" (EventBus.AskPlugin) — mirrors
// tax_hook.go's taxRateAskPayload convention for a value-returning hook.
type exportRequestPayload struct {
	From     string `json:"from"`
	To       string `json:"to"`
	EntryKey string `json:"entry_key"`
	// Sales is the actual sale/tax/payment data for [From, To] (ut-docs#221)
	// -- without it a subscribing export plugin (e.g. a future DATEV or
	// DSFinV-K plugin) has nothing to build a real file from.
	Sales []data.ExportSaleRow `json:"sales"`
	// Stock is current on-hand stock per item/location (ut-docs#59) -- a
	// snapshot as of now, not as of To; there is no stock-movement history
	// to reconstruct a past-dated level from.
	Stock []data.ExportStockRow `json:"stock"`
}

// exportResponse is the JSON a plugin writes to stdout to answer
// "export.requested.ask". Exactly one of ContentB64 (an inline file for the
// till to stream back as a download) or Message (a plain status, e.g. "sent
// to fiskaly") is expected; Error carries the reason when OK is false.
type exportResponse struct {
	OK         bool   `json:"ok"`
	Filename   string `json:"filename,omitempty"`
	ContentB64 string `json:"content_b64,omitempty"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// importRequestPayload is the event payload the resolved import-type
// plugin receives on "import.requested.ask" (EventBus.AskPlugin) —
// exportRequestPayload's counterpart for ut-docs#599. Deliberately tiny:
// FileHandle is a plugin-scoped handle into the host's staged-file
// registry (plugins.OpenImportFile), NEVER the file bytes — the guest
// pulls the content itself in bounded chunks via the import_file_size/
// import_file_read/import_file_close host functions (ADR-0001 amendment
// 2026-08-12), so a hundreds-of-MB upload is never marshaled through the
// JSON event envelope.
type importRequestPayload struct {
	EntryKey string `json:"entry_key"`
	// Entities is the resolved set for this run: requested ∩ declared ∩
	// granted "<entity>:write" (never more than the entry's manifest
	// declares, never an entity the plugin lacks the write grant for).
	Entities   []string `json:"entities"`
	FileHandle int32    `json:"file_handle"`
	FileName   string   `json:"file_name"`
	FileSize   int64    `json:"file_size"`
}

// importResponse is the JSON a plugin writes to stdout to answer
// "import.requested.ask" — exportResponse's counterpart. Counts maps an
// entity name to how many records the plugin reports it parsed/would
// import for that entity; Error carries the reason when OK is false.
type importResponse struct {
	OK      bool           `json:"ok"`
	Message string         `json:"message,omitempty"`
	Error   string         `json:"error,omitempty"`
	Counts  map[string]int `json:"counts,omitempty"`
}

// maxImportFileSize bounds POST /api/data/import's staged upload. Enforced
// authoritatively on BYTES ACTUALLY WRITTEN while streaming to the temp
// file, with a cheap declared-size fast reject in front (the multipart
// header's Size), mirroring catimport's bkpMaxDBSize shape — and, like it,
// a var (not a const) so tests can lower it instead of uploading gigantic
// fixtures. 1GB matches bkpMaxDBSize's rationale: the one real input on
// hand (a speedy kasse .bkp) is 270MB, so 1GB leaves comfortable headroom.
var maxImportFileSize int64 = 1 << 30 // 1GB

// dataAPIRespond writes the { "data": …, "error": null|"…" } envelope
// universal-till/CLAUDE.md mandates (ut-docs#387): on success msg lands
// under data.message with error:null, on failure data is null and error
// carries msg. Shared by /api/data/* handlers (data_api.go,
// import_dispatch.go).
func dataAPIRespond(w http.ResponseWriter, status int, ok bool, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if ok {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"message": msg}, "error": nil})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": msg})
}

// registerDataAPI wires the manager-gated Data-management actions. Clearing
// test data before go-live is all-or-nothing and audited (it cannot cherry-pick
// individual sales). More operations (customer erasure, catalog cleanup) build
// on this pattern.
func registerDataAPI(mux *http.ServeMux, d *common.Deps) {
	respond := dataAPIRespond

	mux.HandleFunc("POST /api/data/reset-transactions", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		if strings.TrimSpace(r.FormValue("confirm")) != "RESET" {
			respond(w, http.StatusBadRequest, false, "type RESET to confirm")
			return
		}
		n, err := data.NewPOSRepo(d.Db).ResetTransactionHistory(r.Context(), auth.UserID(r))
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		// Refresh the in-memory shift/menu state is unnecessary — the basket is
		// in memory and unaffected; the next receipt number restarts from 1.
		respond(w, http.StatusOK, true, fmt.Sprintf("cleared %d sales and related records", n))
	})

	// GDPR: find a customer to erase.
	mux.HandleFunc("GET /api/data/customers", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		list, err := data.NewPOSRepo(d.Db).SearchCustomers(r.Context(), r.URL.Query().Get("q"), 25)
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"customers": list}, "error": nil})
	})

	// GDPR: erase a customer's personal data (keeps their sales, anonymised).
	mux.HandleFunc("POST /api/data/customers/erase", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "" {
			respond(w, http.StatusBadRequest, false, "customer id required")
			return
		}
		ok, err := data.NewPOSRepo(d.Db).EraseCustomer(r.Context(), id, auth.UserID(r))
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		if !ok {
			respond(w, http.StatusNotFound, false, "customer not found")
			return
		}
		respond(w, http.StatusOK, true, "customer erased")
	})

	// Catalog cleanup: preview the inactive, never-sold items that can be removed.
	mux.HandleFunc("GET /api/data/obsolete-items", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		list, err := data.NewPOSRepo(d.Db).ListObsoleteItems(r.Context(), 200)
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": list}, "error": nil})
	})

	// Catalog cleanup: permanently remove the previewed obsolete items.
	mux.HandleFunc("POST /api/data/cleanup-catalog", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		if strings.TrimSpace(r.FormValue("confirm")) != "CLEANUP" {
			respond(w, http.StatusBadRequest, false, "type CLEANUP to confirm")
			return
		}
		n, err := data.NewPOSRepo(d.Db).CleanupObsoleteItems(r.Context(), auth.UserID(r))
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		respond(w, http.StatusOK, true, fmt.Sprintf("removed %d obsolete products", n))
	})

	// Dispatch a date-ranged export/report to a SPECIFIC installed export-
	// or report-type entry (ut-docs#189). The engine's event dispatch is
	// already generic across canonical_type (proven by tax.rate.ask); this
	// is the host-side trigger for export/report entries specifically —
	// mirrors tax_hook.go's use of plugins.SharedBus, but resolves the
	// entry to its owning plugin first and asks that plugin only
	// (AskPlugin, not a broadcast Ask) — a different installed plugin
	// subscribed to the same event name must never be able to answer on
	// another plugin's behalf.
	mux.HandleFunc("POST /api/data/export", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		from := strings.TrimSpace(r.FormValue("from"))
		to := strings.TrimSpace(r.FormValue("to"))
		if from == "" || to == "" {
			respond(w, http.StatusBadRequest, false, "from and to are required (YYYY-MM-DD)")
			return
		}
		fromDate, err := time.Parse("2006-01-02", from)
		if err != nil {
			respond(w, http.StatusBadRequest, false, "from must be YYYY-MM-DD")
			return
		}
		toDate, err := time.Parse("2006-01-02", to)
		if err != nil {
			respond(w, http.StatusBadRequest, false, "to must be YYYY-MM-DD")
			return
		}
		if fromDate.After(toDate) {
			respond(w, http.StatusBadRequest, false, "from must not be after to")
			return
		}
		// ut-docs#229: an unbounded range means an unbounded data-gathering
		// step on till-class (e.g. Pi) hardware before the plugin call even
		// starts — reject before touching the repo layer at all, regardless
		// of how cheap SalesForExport's own query shape is.
		if toDate.Sub(fromDate) > maxExportRange {
			respond(w, http.StatusBadRequest, false, fmt.Sprintf("date range exceeds the %d-day maximum", maxExportRangeDays))
			return
		}

		entries, err := data.NewPluginRepo(d.Db).ListExportEntries(r.Context())
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		if len(entries) == 0 {
			respond(w, http.StatusBadRequest, false, "no export plugin installed")
			return
		}

		entryKey := strings.TrimSpace(r.FormValue("entry_key"))
		var entry data.ExportEntryRow
		switch {
		case entryKey != "":
			found := false
			for _, e := range entries {
				if e.Key == entryKey {
					entry = e
					found = true
					break
				}
			}
			if !found {
				respond(w, http.StatusNotFound, false, "no installed export entry with that key")
				return
			}
		case len(entries) == 1:
			entry = entries[0]
		default:
			respond(w, http.StatusBadRequest, false, "multiple export entries installed — specify entry_key")
			return
		}

		posRepo := data.NewPOSRepo(d.Db)

		// The export payload carries two distinct ledgers (ut-docs#228); each
		// is gated on its own permission rather than one flat check, so a
		// plugin that only declared sales:read never also receives the full
		// stock ledger it never asked for, and vice versa. A plugin missing
		// one or both permissions still gets a real response (never a 4xx
		// here) — omitting a ledger it can't see is not the same thing as
		// the request itself being invalid. CheckPermissionGranted (not
		// CheckPermission) is used deliberately: it separates a genuine
		// infrastructure failure (err != nil, must 500 — silently shipping
		// an empty ledger on a DB fault would look like a real "no data"
		// export) from a legitimate not-declared/not-granted denial
		// (granted=false, err=nil, omit and continue); it still audits the
		// denial the same way CheckPermission does.
		hasSales, err := plugins.CheckPermissionGranted(r.Context(), d.Db, entry.PluginID, "sales:read")
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		var sales []data.ExportSaleRow
		if hasSales {
			// ut-docs#439: reject before the expensive batch gather (and the
			// WASM dispatch after it) if the matched row count exceeds the
			// bound, the same "reject before doing the expensive work" shape
			// the range cap above already uses. A cheap COUNT(*) first, not
			// SalesForExport's own row count, so an over-large match never
			// pays for the full batch gather it's about to be rejected for.
			count, cerr := posRepo.CountSalesForExport(r.Context(), from, to)
			if cerr != nil {
				respond(w, http.StatusInternalServerError, false, cerr.Error())
				return
			}
			if count > maxExportSalesRows {
				respond(w, http.StatusBadRequest, false, fmt.Sprintf("matched sale count (%d) exceeds the %d-row maximum — narrow the date range", count, maxExportSalesRows))
				return
			}
			sales, err = posRepo.SalesForExport(r.Context(), from, to)
			if err != nil {
				respond(w, http.StatusInternalServerError, false, err.Error())
				return
			}
		}

		hasStock, err := plugins.CheckPermissionGranted(r.Context(), d.Db, entry.PluginID, "inventory:read")
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		var stock []data.ExportStockRow
		if hasStock {
			stock, err = posRepo.StockForExport(r.Context())
			if err != nil {
				respond(w, http.StatusInternalServerError, false, err.Error())
				return
			}
		}

		// AskPlugin, not Ask: entry was resolved to a specific owning
		// plugin above, and must not silently accept another installed
		// plugin's answer to the same event type (ut-docs#189 review).
		resp, ok, err := plugins.SharedBus(d.Db).AskPlugin(r.Context(), entry.PluginID, "export.requested.ask", exportRequestPayload{
			From: from, To: to, EntryKey: entry.Key, Sales: sales, Stock: stock,
		})
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		if !ok {
			respond(w, http.StatusBadRequest, false, "export plugin did not respond")
			return
		}

		var parsed exportResponse
		if jerr := json.Unmarshal(resp, &parsed); jerr != nil {
			respond(w, http.StatusInternalServerError, false, "export plugin returned an invalid response")
			return
		}
		if !parsed.OK {
			errMsg := parsed.Error
			if errMsg == "" {
				errMsg = "export plugin declined without a reason"
			}
			respond(w, http.StatusBadRequest, false, errMsg)
			return
		}
		if parsed.ContentB64 != "" {
			raw, derr := base64.StdEncoding.DecodeString(parsed.ContentB64)
			if derr != nil {
				respond(w, http.StatusInternalServerError, false, "export plugin returned invalid content")
				return
			}
			filename := parsed.Filename
			if filename == "" {
				filename = fmt.Sprintf("export-%s-to-%s.bin", from, to)
			}
			ctype := mime.TypeByExtension(filepath.Ext(filename))
			if ctype == "" {
				ctype = "application/octet-stream"
			}
			w.Header().Set("Content-Type", ctype)
			// mime.FormatMediaType quotes/escapes filename safely — a raw
			// `"`+filename+`"` would let a crafted filename close the
			// quoted attribute early (ut-docs#189 review finding).
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(filename)}))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
		msg := parsed.Message
		if msg == "" {
			msg = "export plugin accepted the request"
		}
		respond(w, http.StatusOK, true, msg)
	})

	// POST /api/data/import — the import-type counterpart (ut-docs#599),
	// kept in its own file to spare this one more bulk.
	registerImportDispatch(mux, d)
}
