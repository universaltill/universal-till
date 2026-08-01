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

// registerDataAPI wires the manager-gated Data-management actions. Clearing
// test data before go-live is all-or-nothing and audited (it cannot cherry-pick
// individual sales). More operations (customer erasure, catalog cleanup) build
// on this pattern.
func registerDataAPI(mux *http.ServeMux, d *common.Deps) {
	respond := func(w http.ResponseWriter, status int, ok bool, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "message": msg})
	}

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
		_ = json.NewEncoder(w).Encode(map[string]any{"customers": list})
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
		_ = json.NewEncoder(w).Encode(map[string]any{"items": list})
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
		sales, err := posRepo.SalesForExport(r.Context(), from, to)
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
		}

		stock, err := posRepo.StockForExport(r.Context())
		if err != nil {
			respond(w, http.StatusInternalServerError, false, err.Error())
			return
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
}
