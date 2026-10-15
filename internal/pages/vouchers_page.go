package pages

import (
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Gift vouchers page (ut-docs#4251): one manager page that lists every
// voucher this till knows about, shows one voucher's history, exports the
// list as CSV and — moved here from Settings > Data management — imports
// opening balances (the import API itself, POST /api/vouchers/import, is
// unchanged in import_vouchers_page.go).
//
// Read-only over vouchers/voucher_transactions (internal/data
// voucher_repo.go's ListVouchers/VoucherTransactions) except for one write:
// the manager balance correction, POST /vouchers/{id}/adjust (ut-docs#4253,
// AdjustVoucherBalance — on an additional till made on the main till's
// ledger first, voucher_sync_proxy.go adjustVoucherOnPrimary). Gated on
// "settings", the same action the import always used, server-side on every
// route. POST /vouchers/{id}/top-up (ut-docs#4252) writes nothing: it adds a
// top-up line to the cashier's basket, and the sale that pays for it credits
// the voucher (pos.CompleteSale → data.TopUpVoucher).
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
		// The correction form (ut-docs#4253). Its expected balance is the
		// one the correction will be checked against: on an additional till
		// that is the main till's live balance when it answers (the
		// correction is made on its ledger, and a lagging local mirror
		// would otherwise refuse every correction as "balance changed").
		possible := v.Status != "void" && v.Purpose == data.VoucherPurposeMulti
		allowed := canPerform(d, r, "settings")
		expected := v.BalanceMinor
		if possible && allowed {
			if pv, ok := fetchVoucherFromPrimary(r.Context(), d, voucherProxyClient, id); ok {
				expected = pv.BalanceMinor
			}
		}
		httpx.Render("ui/pages/voucher_detail.html", map[string]any{
			"title":     httpx.T(httpx.RequestLocale(r), "vouchers.title"),
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"voucher":   v,
			"history":   views,
			// ut-docs#4252: the Top up section. Same gate and the same
			// "possible" rule as the correction; an additional till gets a
			// note instead of the form (top-ups are main-till only).
			"topUp": map[string]any{
				"allowed":  allowed,
				"possible": possible,
				"void":     v.Status == "void",
				"replica":  d.SyncPrimaryURL(r.Context()) != "",
				"href":     "/vouchers/" + url.PathEscape(id) + "/top-up",
			},
			"adjust": map[string]any{
				"allowed":  allowed,
				"possible": possible,
				"void":     v.Status == "void",
				"id":       uuid.NewString(),
				"expected": expected,
				"done":     r.URL.Query().Get("adjusted") == "1",
				"href":     "/vouchers/" + url.PathEscape(id) + "/adjust",
			},
		})(w, r)
	})

	// POST /vouchers/{id}/adjust — a manager balance correction with a
	// reason (ut-docs#4253). No money moves; the correction is a signed
	// 'adjustment' ledger row plus an audit row (data.AdjustVoucherBalance).
	// Errors come back as an inline HTML fragment (non-2xx, text/html —
	// app.js swaps it into the form's message slot), success as
	// HX-Redirect / 303 back to the detail page.
	mux.HandleFunc("POST /vouchers/{id}/adjust", func(w http.ResponseWriter, r *http.Request) {
		// With auth on, an audited correction needs a named actor: a
		// session whose user id is empty is refused like a missing
		// permission (review MINOR-5). UT_AUTH=off (canPerform's escape
		// hatch) has no sessions, so it is exempt.
		if !canPerform(d, r, "settings") || (!auth.Disabled(os.Getenv("UT_AUTH")) && strings.TrimSpace(getSessionUserID(r)) == "") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		id := r.PathValue("id")
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			voucherAdjustMessage(w, r, http.StatusBadRequest, "vouchers.adjust.error.reload")
			return
		}
		adjID := strings.TrimSpace(r.PostFormValue("adjustment_id"))
		if _, err := uuid.Parse(adjID); err != nil {
			voucherAdjustMessage(w, r, http.StatusBadRequest, "vouchers.adjust.error.reload")
			return
		}
		expected, err := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("expected_balance")), 10, 64)
		if err != nil {
			// The hidden field is the page's, not the manager's: an
			// unparseable one means a stale or tampered form (review NIT-3).
			voucherAdjustMessage(w, r, http.StatusBadRequest, "vouchers.adjust.error.reload")
			return
		}
		newBal, err := httpx.ParseMoneyMajor(r.PostFormValue("new_balance"), httpx.ActiveCurrency().Decimals)
		if err != nil {
			voucherAdjustMessage(w, r, http.StatusBadRequest, "vouchers.adjust.error.amount")
			return
		}
		in := data.VoucherAdjustment{
			ID:                   adjID,
			VoucherID:            id,
			ExpectedBalanceMinor: expected,
			NewBalanceMinor:      newBal,
			Reason:               r.PostFormValue("reason"),
			ActorUserID:          getSessionUserID(r),
			Now:                  time.Now().UTC().Format(time.RFC3339),
		}

		// An additional till corrects on the main till's ledger first and
		// refuses outright when it can't (never a local-only correction).
		primaryAfter, isReplica, err := adjustVoucherOnPrimary(r.Context(), d, voucherProxyClient, in)
		if err == nil {
			if isReplica {
				if mErr := mirrorVoucherAdjustmentLocally(r.Context(), d, repo, in, primaryAfter); mErr != nil {
					// The main till's ledger has the correction, which is
					// what counts; a failed local mirror is a Problem to
					// look at (Warn lands on the Problems panel, same
					// plumbing as releaseVoucherOnPrimary), not a reason to
					// tell the manager it failed.
					logging.L().Warnf("voucher correction %s (%s): applied on the main till, local mirror failed — this till shows a stale balance until it is next corrected or synced: %v", id, in.ID, mErr)
				}
			} else {
				_, _, err = repo.AdjustVoucherBalance(r.Context(), nil, in)
			}
		}
		if err != nil {
			status, key := voucherAdjustErrorKey(err)
			if status == http.StatusInternalServerError {
				logging.L().Errorf("voucher correction %s: %v", id, err)
			}
			voucherAdjustMessage(w, r, status, key)
			return
		}
		target := "/vouchers/" + url.PathEscape(id) + "?adjusted=1"
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", target)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})

	// POST /vouchers/{id}/top-up — add value to an existing voucher as a
	// sale (ut-docs#4252). It checks the voucher BEFORE touching the basket,
	// then adds a top-up gift-voucher line (Code = this voucher, Price = the
	// amount) to the CASHIER's basket, d.Engine — never the self-order
	// kiosk's — and sends the manager to the sale screen to take payment.
	// Nothing is credited here: the paying sale does that in its own
	// transaction (pos.CompleteSale → data.TopUpVoucher, which re-checks
	// every rule). Errors are an inline fragment, like /adjust.
	mux.HandleFunc("POST /vouchers/{id}/top-up", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.RequestLocale(r)
		fail := func(status int, msg string) { voucherInlineMessage(w, status, msg) }
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// An additional till can't top up yet: its sale would have to
		// credit the main till's ledger (follow-up card). Checkout itself is
		// untouched — this only refuses adding the line.
		if d.SyncPrimaryURL(r.Context()) != "" {
			fail(http.StatusConflict, httpx.T(locale, "vouchers.top_up.replica_note"))
			return
		}
		if d.Engine == nil {
			fail(http.StatusInternalServerError, httpx.T(locale, "common.error.server"))
			return
		}
		id := r.PathValue("id")
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		amountMsg := fmt.Sprintf(httpx.T(locale, "vouchers.top_up.error.amount"), httpx.FormatMoneyDisplay(pos.MaxVoucherIssueAmount.Minor(), locale))
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, amountMsg)
			return
		}
		amount, err := httpx.ParseMoneyMajor(r.PostFormValue("amount"), httpx.ActiveCurrency().Decimals)
		if err != nil || amount <= 0 || money.FromMinor(amount) > pos.MaxVoucherIssueAmount {
			fail(http.StatusBadRequest, amountMsg)
			return
		}
		v, err := repo.GetVoucherBalance(r.Context(), nil, id)
		switch {
		case errors.Is(err, data.ErrVoucherNotFound):
			fail(http.StatusNotFound, httpx.T(locale, "vouchers.error.not_found"))
			return
		case err != nil:
			logging.L().Errorf("voucher top-up %s: %v", id, err)
			fail(http.StatusInternalServerError, httpx.T(locale, "common.error.server"))
			return
		case v.Status == "void":
			fail(http.StatusConflict, httpx.T(locale, "vouchers.top_up.not_void"))
			return
		case v.Purpose != data.VoucherPurposeMulti:
			fail(http.StatusConflict, httpx.T(locale, "vouchers.top_up.not_single"))
			return
		case v.BalanceMinor > pos.MaxVoucherIssueAmount.Minor()-amount:
			fail(http.StatusBadRequest, amountMsg)
			return
		}
		if d.Engine.HasGiftVoucherCode(v.ID) {
			fail(http.StatusConflict, httpx.T(locale, "vouchers.top_up.error.in_sale"))
			return
		}
		if countGiftVoucherLines(d.Engine.Lines()) >= pos.MaxVoucherIssuesPerSale {
			fail(http.StatusConflict, fmt.Sprintf(httpx.T(locale, "pos.gift_voucher.error.too_many"), pos.MaxVoucherIssuesPerSale))
			return
		}
		// The line's base is the shop's gift voucher item, resolved the way
		// the sale screen resolves a tile (its item-id code), so the line
		// is exactly what selling a voucher puts in the basket.
		itemID, ok, err := data.NewCatalogRepo(d.Db).ActiveGiftVoucherItemID(r.Context())
		if err != nil {
			logging.L().Errorf("voucher top-up %s: gift voucher item: %v", id, err)
			fail(http.StatusInternalServerError, httpx.T(locale, "common.error.server"))
			return
		}
		var base pos.BasketLine
		if ok {
			base, ok = d.Engine.ResolveExact(data.ItemIDCode(itemID))
		}
		if !ok || !base.IsGiftVoucherItem() {
			fail(http.StatusConflict, httpx.T(locale, "vouchers.top_up.error.no_item"))
			return
		}
		_, err = d.Engine.AddGiftVoucherLine(base, money.FromMinor(amount), pos.GiftVoucherLine{
			Code:        v.ID,
			HolderLabel: boundGiftVoucherHolder(v.HolderLabel),
			Purpose:     data.VoucherPurposeMulti,
			TopUp:       true,
		})
		if errors.Is(err, pos.ErrGiftVoucherCodeInBasket) {
			fail(http.StatusConflict, httpx.T(locale, "vouchers.top_up.error.in_sale"))
			return
		}
		if err != nil {
			logging.L().Errorf("voucher top-up %s: add line: %v", id, err)
			fail(http.StatusInternalServerError, httpx.T(locale, "common.error.server"))
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
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

// voucherAdjustErrorKey maps a correction failure to its status and the
// localized message shown inline on the detail page (ut-docs#4253).
func voucherAdjustErrorKey(err error) (int, string) {
	switch {
	case errors.Is(err, errVoucherPrimaryUnreachable):
		return http.StatusServiceUnavailable, "vouchers.adjust.error.primary_unreachable"
	case errors.Is(err, errVoucherPrimaryUnknown):
		return http.StatusBadGateway, "vouchers.adjust.error.primary_unknown"
	case errors.Is(err, data.ErrVoucherNotFound):
		return http.StatusNotFound, "vouchers.error.not_found"
	case errors.Is(err, data.ErrVoucherAdjustmentInvalid):
		return http.StatusBadRequest, "vouchers.adjust.error.amount"
	case errors.Is(err, data.ErrVoucherAdjustmentReason):
		return http.StatusBadRequest, "vouchers.adjust.error.reason"
	case errors.Is(err, data.ErrVoucherAdjustmentNoChange):
		return http.StatusConflict, "vouchers.adjust.error.no_change"
	case errors.Is(err, data.ErrVoucherBalanceChanged):
		return http.StatusConflict, "vouchers.adjust.error.changed"
	case errors.Is(err, data.ErrVoucherNotActive):
		return http.StatusConflict, "vouchers.adjust.not_void"
	case errors.Is(err, data.ErrVoucherNotMultiPurpose):
		return http.StatusConflict, "vouchers.adjust.not_single"
	}
	return http.StatusInternalServerError, "common.error.server"
}

// voucherAdjustMessage writes the inline (non-modal) message fragment the
// correction form swaps into its message slot.
func voucherAdjustMessage(w http.ResponseWriter, r *http.Request, status int, key string) {
	voucherInlineMessage(w, status, httpx.T(httpx.RequestLocale(r), key))
}

// voucherInlineMessage writes an already-localized message as the inline
// fragment the detail page's forms swap into their message slot.
func voucherInlineMessage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<div class="notice-block-warn" role="alert"><p>%s</p></div>`, html.EscapeString(msg))
}
