package pages

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Gift vouchers page (ut-docs#4251): one manager page that lists every
// voucher this till knows about, shows one voucher's history, exports the
// list as CSV and — moved here from Settings > Data management — imports
// opening balances (the import API itself, POST /api/vouchers/import, is
// unchanged in import_vouchers_page.go).
//
// Read-only over vouchers/voucher_transactions (internal/data
// voucher_repo.go's ListVouchers/VoucherTransactions). Gated on "settings",
// the same action the import always used, server-side on every route.
//
// Offline-first: everything reads the local database. On an additional
// till that local view is only the vouchers this till has seen (issued
// here, imported here, or redeemed here via the cross-till write-through,
// ADR-0084), so the page says so with a non-modal status banner rather
// than proxying to the main till.

// vouchersPageLimit bounds the on-screen list (the export is unbounded).
const vouchersPageLimit = 200

// vouchersExportHeader is the export's header row. code/balance/label are
// exactly the importer's own column names (catimport.ParseVoucherBalances),
// so the file round-trips; purpose lets the importer refuse single-purpose
// rows; the rest is ignored by the importer.
var vouchersExportHeader = []string{"code", "balance", "label", "original_amount", "status", "purpose", "created_at"}

// voucherRowView is one list row as the template needs it.
type voucherRowView struct {
	data.VoucherListRow
	DetailHref string // /vouchers/{id}, path-escaped
	SaleHref   string // /journal/{receipt} when the issuing sale is still on this till
	Imported   bool   // no issuing sale at all: an opening balance from the import
}

// voucherHistoryView is one history row as the template needs it.
type voucherHistoryView struct {
	data.VoucherHistoryRow
	SaleHref string
}

// voucherFilterFromQuery reads q/status. An unknown status falls back to
// "all" (it is a URL anyone can edit; ListVouchers itself refuses one).
func voucherFilterFromQuery(r *http.Request) data.VoucherFilter {
	q := r.URL.Query()
	f := data.VoucherFilter{Query: strings.TrimSpace(q.Get("q"))}
	if rs := []rune(f.Query); len(rs) > 100 {
		f.Query = string(rs[:100])
	}
	switch s := q.Get("status"); s {
	case "active", "redeemed", "void":
		f.Status = s
	}
	return f
}

func journalHref(receiptNo string) string {
	if receiptNo == "" {
		return ""
	}
	return "/journal/" + url.PathEscape(receiptNo)
}

func registerVouchers(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPOSRepo(d.Db)

	// listData builds the list block's data (shared by the page's first
	// render and the htmx fragment). ok=false means an error page was
	// already written.
	listData := func(w http.ResponseWriter, r *http.Request, f data.VoucherFilter) (map[string]any, bool) {
		f.Limit = vouchersPageLimit + 1 // one extra row tells us the list was cut
		rows, err := repo.ListVouchers(r.Context(), f)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return nil, false
		}
		truncated := len(rows) > vouchersPageLimit
		if truncated {
			rows = rows[:vouchersPageLimit]
		}
		views := make([]voucherRowView, 0, len(rows))
		for _, row := range rows {
			views = append(views, voucherRowView{
				VoucherListRow: row,
				DetailHref:     "/vouchers/" + url.PathEscape(row.ID),
				SaleHref:       journalHref(row.IssuedReceiptNo),
				Imported:       row.IssuedSaleID == "",
			})
		}
		params := url.Values{}
		if f.Query != "" {
			params.Set("q", f.Query)
		}
		if f.Status != "" {
			params.Set("status", f.Status)
		}
		exportHref := "/api/vouchers/export"
		if enc := params.Encode(); enc != "" {
			exportHref += "?" + enc
		}
		return map[string]any{
			"vouchers":   views,
			"filtered":   f.Query != "" || f.Status != "",
			"truncated":  truncated,
			"limit":      vouchersPageLimit,
			"exportHref": exportHref,
		}, true
	}

	mux.HandleFunc("GET /vouchers", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		f := voucherFilterFromQuery(r)
		list, ok := listData(w, r, f)
		if !ok {
			return
		}
		page := map[string]any{
			"title":       httpx.T(httpx.RequestLocale(r), "vouchers.title"),
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"SyncPrimary": d.SyncPrimaryURL(r.Context()),
			"q":           f.Query,
			"status":      f.Status,
			"list":        list,
		}
		httpx.Render("ui/pages/vouchers.html", page)(w, r)
	})

	// The list block alone, for the search box / status filter's htmx swap.
	mux.HandleFunc("GET /ui/vouchers/list", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		list, ok := listData(w, r, voucherFilterFromQuery(r))
		if !ok {
			return
		}
		httpx.RenderBlock("ui/pages/vouchers.html", "vouchers_list", map[string]any{"list": list})(w, r)
	})

	mux.HandleFunc("GET /vouchers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		// The id is operator-typed text: it only ever reaches SQL as a bound
		// parameter and the page through html/template's escaping.
		id := r.PathValue("id")
		v, err := repo.GetVoucherBalance(r.Context(), nil, id)
		if errors.Is(err, data.ErrVoucherNotFound) {
			httpx.RenderError(w, r, http.StatusNotFound, "vouchers.error.not_found", nil)
			return
		}
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		hist, err := repo.VoucherTransactions(r.Context(), id)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		views := make([]voucherHistoryView, 0, len(hist))
		for _, h := range hist {
			views = append(views, voucherHistoryView{VoucherHistoryRow: h, SaleHref: journalHref(h.ReceiptNo)})
		}
		httpx.Render("ui/pages/voucher_detail.html", map[string]any{
			"title":     httpx.T(httpx.RequestLocale(r), "vouchers.title"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"voucher":   v,
			"history":   views,
		})(w, r)
	})

	mux.HandleFunc("GET /api/vouchers/export", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		f := voucherFilterFromQuery(r) // Limit 0: the export is never cut to the page bound
		rows, err := repo.ListVouchers(r.Context(), f)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "vouchers-export", err)
			return
		}
		// Same decimals the importer parses with (import_vouchers_page.go).
		decimals := httpx.ActiveCurrency().Decimals
		filename := "vouchers-" + time.Now().Format("2006-01-02") + ".csv"
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write(vouchersExportHeader)
		for _, v := range rows {
			// code and label are operator-typed: csvSafe defuses a leading
			// formula trigger, and the importer strips that same "'" back
			// off (catimport.stripCSVDefuse). One lossy edge: a stored value
			// already shaped "'=X" comes back as "=X" (stripCSVDefuse doc,
			// ut-docs#321).
			_ = cw.Write([]string{
				csvSafe(v.ID),
				httpx.FormatMajorPlain(v.BalanceMinor, decimals),
				csvSafe(v.HolderLabel),
				httpx.FormatMajorPlain(v.OriginalAmountMinor, decimals),
				v.Status,
				v.Purpose,
				v.CreatedAt,
			})
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			logging.L().Warnf("[vouchers-export] write: %v", err)
		}
		_ = repo.InsertAudit(r.Context(), nil, getSessionUserID(r), "voucher", "-", "export",
			map[string]any{"rows": len(rows), "q": f.Query, "status": f.Status}, time.Now().UTC().Format(time.RFC3339), "")
	})
}
