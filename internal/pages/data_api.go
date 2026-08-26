package pages

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
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
	// Items is the full catalog (data.CatalogRepo.ExportRows -- the same
	// rows GET /api/catalog/export's hardcoded CSV writer already reads),
	// gated on both the resolved entry declaring "items" in its Entities
	// AND holding items:read (ut-docs#600) -- unlike Sales/Stock, which
	// are permission-gated only, Items is the first ledger to require an
	// explicit entity declaration too, mirroring how import entries have
	// declared their handled entities since ut-docs#599.
	Items []data.ExportRow `json:"items"`
	// TaxCodes mirrors Items' entity+permission gating (ut-docs#655): the
	// resolved entry must declare "tax_codes" in its Entities AND hold
	// tax_codes:read, reusing data.CatalogRepo.ListAllTaxCodes (already
	// backing the tax-code management UI, ut-docs#259) rather than adding
	// a new listing method. ut-docs#655's own ticket text said
	// "catalog:read", but seedExportPluginWithEntities' doc comment
	// records #600 review finding F2: an earlier draft used exactly that
	// name and it was rejected for breaking the established
	// <entity>:<verb> permission convention (items:read, sales:read, ...)
	// AND colliding with an unrelated "catalog:read" already defined in
	// ut-cloud for marketplace-catalog access. tax_codes:read follows the
	// same convention items:read does.
	TaxCodes []data.TaxCodeView `json:"tax_codes"`
	// EODCloses is every archived day-close's payment-method x VAT-rate
	// cross-tab in [From, To] (ut-docs#1005) -- gated on the entry declaring
	// "eod_closes" in its Entities, mirroring Items/TaxCodes' declare+
	// permission-gated pattern. sales:read is the permission (the same
	// ledger this data is derived from) -- deliberately NOT a new
	// permission name. Read from the ALREADY-ARCHIVED, immutable Z-report
	// rows (data.EODClosesForExport), never recomputed fresh, so an
	// accounting batch built from it reconciles to the merchant's Z-reports
	// by construction.
	//
	// Deliberately NO omitempty: EODClosesForExport returns a non-nil empty
	// slice specifically so "[]" (supported host, zero archived closes in
	// range) stays wire-distinguishable from null (entity not declared /
	// permission not granted / pre-#1005 host). A plugin routes on that
	// difference -- "[]" means "refuse with a clear no-closes error", null
	// means "fall back to the legacy per-sale grain" -- and omitempty would
	// collapse both into absence.
	EODCloses []data.EODCloseExport `json:"eod_closes"`
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
		if !canPerform(d, r, "data_management") {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		if strings.TrimSpace(r.FormValue("confirm")) != "RESET" {
			respond(w, http.StatusBadRequest, false, "type RESET to confirm")
			return
		}
		n, batchID, err := data.NewPOSRepo(d.Db).ResetTransactionHistory(r.Context(), auth.UserID(r))
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		// Refresh the in-memory shift/menu state is unnecessary — the basket is
		// in memory and unaffected; the next receipt number restarts from 1.
		// ADR-0042: nothing was destroyed — say so.
		respond(w, http.StatusOK, true, fmt.Sprintf("archived %d sales and related records (batch %s) — restorable from Settings → Data until the till trades again", n, batchID))
	})

	// ADR-0042: list the archived reset batches, newest first.
	mux.HandleFunc("GET /api/data/reset-archives", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "data_management") {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		list, err := data.NewPOSRepo(d.Db).ListResetBatches(r.Context())
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		type batchJSON struct {
			ID            string `json:"id"`
			CreatedAt     string `json:"created_at"`
			ActorID       string `json:"actor_id"`
			SalesCount    int64  `json:"sales_count"`
			Purgeable     bool   `json:"purgeable"`
			RetainedUntil string `json:"retained_until,omitempty"`
		}
		batches := make([]batchJSON, 0, len(list))
		for _, b := range list {
			bj := batchJSON{ID: b.ID, CreatedAt: b.CreatedAt, ActorID: b.ActorID, SalesCount: b.SalesCount, Purgeable: b.Purgeable}
			// ut-docs#698: RetainedUntil is the zero time.Time whenever no
			// retention gate applied at all (SalesCount == 0) -- omit
			// rather than render a meaningless "retained until 0001-01-01".
			if !b.RetainedUntil.IsZero() {
				bj.RetainedUntil = b.RetainedUntil.Format("2006-01-02")
			}
			batches = append(batches, bj)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batches": batches}, "error": nil})
	})

	// ADR-0042 §2: restore one archived reset batch, whole-batch only,
	// refusing (409) if the till has traded since the reset, or (422) if
	// the batch references a catalog/customer record removed after the
	// reset (independent review, ut-docs#187 — see
	// data.ErrArchiveReferencesRemoved's doc comment). Gated exactly like
	// reset itself: manager + its own typed confirmation.
	mux.HandleFunc("POST /api/data/reset-archives/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "data_management") {
			respond(w, http.StatusForbidden, false, "manager only")
			return
		}
		_ = r.ParseForm()
		if strings.TrimSpace(r.FormValue("confirm")) != "RESTORE" {
			respond(w, http.StatusBadRequest, false, "type RESTORE to confirm")
			return
		}
		n, err := data.NewPOSRepo(d.Db).RestoreResetBatch(r.Context(), r.PathValue("id"), auth.UserID(r))
		if err != nil {
			switch {
			case errors.Is(err, data.ErrResetBatchNotFound):
				respond(w, http.StatusNotFound, false, "reset archive batch not found")
			case errors.Is(err, data.ErrShopHasTradedSinceReset):
				respond(w, http.StatusConflict, false, "the till has traded since this reset — the archived batch can no longer be restored automatically")
			case errors.Is(err, data.ErrArchiveReferencesRemoved):
				respond(w, http.StatusUnprocessableEntity, false, "this archive references a product, stock location or customer that was removed or erased after the reset and can no longer be restored automatically")
			default:
				respond(w, http.StatusInternalServerError, false, err.Error())
			}
			return
		}
		respond(w, http.StatusOK, true, fmt.Sprintf("restored %d sales and related records", n))
	})

	// ADR-0042 §3 / ut-docs#661: permanently purge one archived reset
	// batch. Gated exactly like restore (manager + a fresh typed
	// confirmation), plus the retention-window check DeleteResetBatch
	// enforces in the repository itself — this handler cannot route around
	// it, only surface what it decided.
	mux.HandleFunc("POST /api/data/reset-archives/{id}/purge", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		if !canPerform(d, r, "data_management") {
			respond(w, http.StatusForbidden, false, httpx.T(locale, "settings.data.archives_purge_manager_only"))
			return
		}
		_ = r.ParseForm()
		if strings.TrimSpace(r.FormValue("confirm")) != "PURGE" {
			respond(w, http.StatusBadRequest, false, httpx.T(locale, "settings.data.archives_purge_confirm_required"))
			return
		}
		err := data.NewPOSRepo(d.Db).DeleteResetBatch(r.Context(), r.PathValue("id"), auth.UserID(r))
		if err != nil {
			var within *data.ArchiveWithinRetentionWindowError
			switch {
			case errors.Is(err, data.ErrResetBatchNotFound):
				respond(w, http.StatusNotFound, false, httpx.T(locale, "settings.data.archives_purge_not_found"))
			case errors.As(err, &within):
				respond(w, http.StatusConflict, false, fmt.Sprintf(
					httpx.T(locale, "settings.data.archives_purge_retained_until"), within.RetainedUntil.Format("2006-01-02")))
			default:
				respond(w, http.StatusInternalServerError, false, err.Error())
			}
			return
		}
		respond(w, http.StatusOK, true, httpx.T(locale, "settings.data.archives_purge_done"))
	})

	// GDPR: find a customer to erase.
	mux.HandleFunc("GET /api/data/customers", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "data_management") {
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
		if !canPerform(d, r, "data_management") {
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
		if !canPerform(d, r, "data_management") {
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
		if !canPerform(d, r, "data_management") {
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
		if !canPerform(d, r, "data_management") {
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

		// EODCloses (ut-docs#1005) mirrors Items/TaxCodes' shape: gated on
		// the entry DECLARING "eod_closes" in its Entities in addition to a
		// permission grant, so an entry installed before #1005 keeps
		// getting exactly today's payload. The permission is sales:read --
		// the closes are derived from the same sales ledger -- so the
		// grant resolved above is reused directly rather than re-checked
		// (a second CheckPermissionGranted call would audit the same
		// denial twice for no extra safety). Resolved BEFORE the sales
		// gather below because an entry that declares "eod_closes" never
		// receives Sales at all: closes are the grain it actually books
		// (one row per day-close), so the per-sale ledger -- and, crucially,
		// the maxExportSalesRows cap sized for it -- must not apply. A
		// full-year closes export (~365 closes) over a busy shop's >50,000
		// sales is the flagship use case, and capping it on a count of rows
		// it never reads would 400 it for nothing. The closes query itself
		// is cheap and needs no equivalent cap (a year is ~365 rows).
		wantsEODCloses := false
		for _, e := range entry.Entities {
			if e == "eod_closes" {
				wantsEODCloses = true
				break
			}
		}
		var eodCloses []data.EODCloseExport
		if wantsEODCloses && hasSales {
			eodCloses, err = posRepo.EODClosesForExport(r.Context(), from, to)
			if err != nil {
				respond(w, http.StatusInternalServerError, false, err.Error())
				return
			}
		}

		var sales []data.ExportSaleRow
		if hasSales && !wantsEODCloses {
			// ut-docs#439: reject before the expensive batch gather (and the
			// WASM dispatch after it) if the matched row count exceeds the
			// bound, the same "reject before doing the expensive work" shape
			// the range cap above already uses. A cheap COUNT(*) first, not
			// SalesForExport's own row count, so an over-large match never
			// pays for the full batch gather it's about to be rejected for.
			// Skipped entirely (cap included) for an eod_closes entry -- see
			// the comment above.
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

		// Items (ut-docs#600) is gated on the entry DECLARING "items" (its
		// Entities, mirroring #599's import-side pattern) in addition to
		// the items:read permission grant -- unlike Sales/Stock above,
		// which are permission-gated only, since every export entry
		// installed before #600 has no Entities at all and must keep
		// getting exactly today's Sales/Stock-only payload regardless of
		// what permissions it happens to hold.
		wantsItems := false
		for _, e := range entry.Entities {
			if e == "items" {
				wantsItems = true
				break
			}
		}
		var items []data.ExportRow
		if wantsItems {
			hasItemsRead, cerr := plugins.CheckPermissionGranted(r.Context(), d.Db, entry.PluginID, "items:read")
			if cerr != nil {
				respond(w, http.StatusInternalServerError, false, cerr.Error())
				return
			}
			if hasItemsRead {
				items, err = data.NewCatalogRepo(d.Db).ExportRows(r.Context())
				if err != nil {
					respond(w, http.StatusInternalServerError, false, err.Error())
					return
				}
			}
		}

		// TaxCodes (ut-docs#655) mirrors Items' shape exactly: gated on the
		// entry DECLARING "tax_codes" in its Entities in addition to the
		// tax_codes:read permission grant (see exportRequestPayload's field
		// comment above for why tax_codes:read, not the ticket's original
		// catalog:read), so an entry installed before #655 (no "tax_codes"
		// declared) keeps getting exactly today's payload regardless of
		// what permissions it happens to hold.
		wantsTaxCodes := false
		for _, e := range entry.Entities {
			if e == "tax_codes" {
				wantsTaxCodes = true
				break
			}
		}
		var taxCodes []data.TaxCodeView
		if wantsTaxCodes {
			hasTaxCodesRead, cerr := plugins.CheckPermissionGranted(r.Context(), d.Db, entry.PluginID, "tax_codes:read")
			if cerr != nil {
				respond(w, http.StatusInternalServerError, false, cerr.Error())
				return
			}
			if hasTaxCodesRead {
				taxCodes, err = data.NewCatalogRepo(d.Db).ListAllTaxCodes(r.Context())
				if err != nil {
					respond(w, http.StatusInternalServerError, false, err.Error())
					return
				}
			}
		}

		// AskPlugin, not Ask: entry was resolved to a specific owning
		// plugin above, and must not silently accept another installed
		// plugin's answer to the same event type (ut-docs#189 review).
		resp, ok, err := plugins.SharedBus(d.Db).AskPlugin(r.Context(), entry.PluginID, "export.requested.ask", exportRequestPayload{
			From: from, To: to, EntryKey: entry.Key, Sales: sales, Stock: stock, Items: items, TaxCodes: taxCodes,
			EODCloses: eodCloses,
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
