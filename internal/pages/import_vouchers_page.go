package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Opening voucher-balance import (ut-docs#1834): a pilot merchant migrating
// from another system has customers holding physical voucher cards with
// real outstanding balances. This page lets a manager bring those balances
// into THIS till's voucher-liability system (ut-docs#1008) as an OPENING
// LIABILITY — never a sale — via a small, header-detected CSV
// (internal/catimport/voucher_balances.go): code, balance, and an optional
// holder label.
//
// Deliberately NOT the full catalog-import wizard's cross-page-load staged-
// upload UX (import_stage.go) — this format is two columns and a shop
// realistically has, at most, a few thousand rows of it. The preview
// fragment instead embeds the raw uploaded bytes back to the browser as a
// hidden base64 form field, and the "Confirm import" button's follow-up
// POST (commit=1) carries that byte-identical field back — no second
// browse, no server-side staging registry, no TTL/cleanup to reason about.
// This is safe specifically BECAUSE of the small upload cap below: at
// 256KB raw, the base64 round-trip is at most ~342KB of hidden form
// field, comfortably inside any request-body limit already in force
// elsewhere in this app.

// maxVoucherImportUploadBytes bounds the raw uploaded CSV. Deliberately much
// smaller than the catalog importer's cap (20MB, import_page.go) — that one
// has to accommodate product images; this format is plain text,
// "code,balance,label" per line, and even a merchant with several thousand
// physical voucher cards outstanding comfortably fits inside this. Bounding
// it small also keeps the preview's embedded-base64-bytes round-trip (see
// the package doc above) a small, uneventful request body.
const maxVoucherImportUploadBytes = 256 << 10 // 256KB

// voucherImportFormOverhead is the slack allowed on top of
// maxVoucherImportUploadBytes for multipart framing and the small
// commit/file_b64 form fields when bounding the whole request body up
// front (http.MaxBytesReader) — mirrors import_dispatch.go's
// importFormOverhead. The base64 encoding of the raw cap itself needs
// ~4/3 the raw bytes, so this must comfortably cover that expansion, not
// just framing noise.
const voucherImportFormOverhead = 128 << 10 // 128KB

// voucherImportIssueCodeExists is this page's own reason code (mirrors
// catimport's VoucherBalanceIssue* consts, same reason-code convention) for
// a commit-time collision: a row that parsed cleanly but whose code already
// belongs to an existing voucher (data.ErrVoucherIDExists). Distinct from
// catimport.VoucherBalanceIssueDuplicateCodeInFile — that's two rows
// colliding with EACH OTHER inside this file; this is one row colliding
// with a voucher that already exists in the database from before this
// import (a prior import, or a voucher this till already issued/sold).
const voucherImportIssueCodeExists = "code_exists"

// registerVoucherImport mounts GET /settings/vouchers/import (the page) and
// POST /api/vouchers/import (preview + commit) — ut-docs#1834.
func registerVoucherImport(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /settings/vouchers/import", func(w http.ResponseWriter, r *http.Request) {
		// "settings" — same gate settings_page.go's own sub-pages use, NOT
		// "import_export" (the catalog-import permission): this is a
		// distinct capability from importing the product catalog.
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		httpx.Render("ui/pages/voucher_import.html", map[string]any{
			"title":     "Import voucher balances",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		})(w, r)
	})

	mux.HandleFunc("POST /api/vouchers/import", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !canPerform(d, r, "settings") {
			// WriteHeader before RenderNotice on every error branch below:
			// RenderNotice only ever writes the notice BODY (it takes a
			// plain io.Writer, not a ResponseWriter, precisely because
			// several callers write a non-200 status first — see
			// import_page.go's replica-conflict branch for the same
			// pattern) — an omitted WriteHeader here would silently answer
			// a forbidden/invalid request with the default 200 OK.
			w.WriteHeader(http.StatusForbidden)
			httpx.RenderNotice(w, locale, "error", "common.error.manager_or_admin_required")
			return
		}
		// http.MaxBytesReader must run before ParseMultipartForm (same
		// ordering issue_report_page.go's own comment explains): once a
		// file part exceeds ParseMultipartForm's in-memory budget it spills
		// the remainder to a temp file with no size check of its own.
		r.Body = http.MaxBytesReader(w, r.Body, maxVoucherImportUploadBytes+voucherImportFormOverhead)
		if err := r.ParseMultipartForm(maxVoucherImportUploadBytes); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			httpx.RenderNotice(w, locale, "error", "vouchers_import.error.invalid_upload")
			return
		}
		commit := r.FormValue("commit") == "1"

		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)

		// Which bytes does this request act on? A commit carrying the
		// preview's embedded file_b64 field reuses those EXACT bytes (see
		// package doc above); everything else (a preview, or a direct
		// first-time commit that skipped preview) reads the uploaded file.
		var raw []byte
		if b64 := r.FormValue("file_b64"); commit && b64 != "" {
			decoded, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				w.WriteHeader(http.StatusBadRequest)
				httpx.RenderNotice(w, locale, "error", "vouchers_import.error.invalid_upload")
				return
			}
			raw = decoded
		} else {
			f, _, ferr := r.FormFile("file")
			if ferr != nil {
				w.WriteHeader(http.StatusBadRequest)
				httpx.RenderNotice(w, locale, "error", "vouchers_import.error.file_required")
				return
			}
			defer f.Close()
			buf, rerr := io.ReadAll(f)
			if rerr != nil {
				w.WriteHeader(http.StatusBadRequest)
				httpx.RenderNotice(w, locale, "error", "vouchers_import.error.invalid_upload")
				return
			}
			raw = buf
		}

		decimals := httpx.ActiveCurrency().Decimals
		res, perr := catimport.ParseVoucherBalances(bytes.NewReader(raw), decimals)
		if perr != nil {
			w.WriteHeader(http.StatusBadRequest)
			if errors.Is(perr, catimport.ErrNoCodeColumn) {
				httpx.RenderNotice(w, locale, "error", "vouchers_import.error.no_code_column")
				return
			}
			logging.L().Infof("[vouchers-import] parse: %v", perr)
			httpx.RenderNotice(w, locale, "error", "vouchers_import.error.invalid_upload")
			return
		}

		if !commit {
			renderVoucherImportPreview(w, locale, T, res, raw)
			return
		}

		imported, importedTotal, collisions, cerr := commitVoucherBalanceImport(r.Context(), d.Db, posRepo, res.Items)
		if cerr != nil {
			logging.L().Errorf("[vouchers-import] commit: %v", cerr)
			w.WriteHeader(http.StatusInternalServerError)
			httpx.RenderNotice(w, locale, "error", "vouchers_import.error.server")
			return
		}
		allIssues := append(append([]catimport.VoucherBalanceRowIssue{}, res.Issues...), collisions...)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "voucher", "-", "import",
			map[string]any{"rows": imported, "rejected": len(allIssues)}, time.Now().UTC().Format(time.RFC3339), "")
		renderVoucherImportResult(w, locale, T, imported, importedTotal, allIssues)
	})
}

// commitVoucherBalanceImport writes every clean item as an opening
// liability inside ONE transaction (ut-docs#1834's design): CreateVoucher +
// RecordVoucherTransaction(type="issue"), mirroring exactly how
// pos.CompleteSale's own VoucherIssues loop calls these two repository
// methods (internal/pos/sales.go) — EXCEPT IssuedSaleID/SaleID stay empty,
// since this is an opening balance with no sale.
//
// A colliding code (data.ErrVoucherIDExists) is caught PER ROW and does
// NOT abort the whole transaction or roll back rows already written in it:
// SQLite's default ABORT conflict-resolution behaviour backs out only the
// one failed INSERT, leaving every prior write in this same tx intact and
// the tx itself still fully usable for the next iteration (verified
// empirically against this app's own modernc.org/sqlite driver — a bare
// SAVEPOINT is not needed here the way RecordStockMovementSavepoint needs
// one in pos_repo.go, because CreateVoucher's failure is a single
// statement, not several). Any OTHER error aborts the whole batch (rolled
// back), same as any other multi-row import commit in this codebase.
func commitVoucherBalanceImport(ctx context.Context, db *sql.DB, repo *data.POSRepo, items []catimport.VoucherBalanceItem) (imported int, importedTotalMinor int64, collisions []catimport.VoucherBalanceRowIssue, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, beginErr := db.BeginTx(ctx, nil)
	if beginErr != nil {
		return 0, 0, nil, fmt.Errorf("begin voucher import transaction: %w", beginErr)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	for _, it := range items {
		createErr := repo.CreateVoucher(ctx, tx, data.Voucher{
			ID:                  it.Code,
			HolderLabel:         it.HolderLabel,
			OriginalAmountMinor: it.BalanceMinor,
			BalanceMinor:        it.BalanceMinor,
			Currency:            httpx.ActiveCurrency().Code,
			// IssuedSaleID deliberately left empty: an opening balance has
			// no sale that issued it (ut-docs#1834).
			CreatedAt: now,
		})
		if createErr != nil {
			if errors.Is(createErr, data.ErrVoucherIDExists) {
				// Never overwrite an existing voucher's balance — skip this
				// row, report it, keep going with the rest of the batch.
				collisions = append(collisions, catimport.VoucherBalanceRowIssue{RowNum: it.RowNum, Code: it.Code, Reason: voucherImportIssueCodeExists})
				continue
			}
			return 0, 0, nil, fmt.Errorf("create voucher %q: %w", it.Code, createErr)
		}
		if txErr := repo.RecordVoucherTransaction(ctx, tx, data.VoucherTransaction{
			ID:        uuid.NewString(),
			VoucherID: it.Code,
			// SaleID deliberately left empty (see CreateVoucher call above)
			// — this is exactly the sale_id IS NULL shape
			// VouchersIssuedRedeemedForRange/ForInstantWindow now exclude
			// from a day's "Issued" flow (internal/data/voucher_repo.go).
			Type:        "issue",
			AmountMinor: it.BalanceMinor,
			CreatedAt:   now,
		}); txErr != nil {
			return 0, 0, nil, fmt.Errorf("record voucher transaction %q: %w", it.Code, txErr)
		}
		imported++
		importedTotalMinor += it.BalanceMinor
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return 0, 0, nil, fmt.Errorf("commit voucher import: %w", commitErr)
	}
	committed = true
	return imported, importedTotalMinor, collisions, nil
}

// renderVoucherImportPreview writes nothing to the database — it renders
// row count, total liability, and every rejected row with a translated
// reason, plus a "Confirm import" form that carries the SAME uploaded bytes
// back (base64, hidden field — see this file's package doc for why that's
// safe at this upload's size cap).
func renderVoucherImportPreview(w http.ResponseWriter, locale string, T func(string) string, res catimport.VoucherBalanceResult, raw []byte) {
	var total int64
	for _, it := range res.Items {
		total += it.BalanceMinor
	}
	var b bytes.Buffer
	b.WriteString(`<div class="card" id="vouchers-import-preview">`)
	fmt.Fprintf(&b, `<p>%s</p>`, htmlEscape(fmt.Sprintf(T("vouchers_import.preview_summary"), len(res.Items), httpx.FormatMoney(total, locale))))
	writeVoucherIssuesTable(&b, T, res.Issues)
	if len(res.Items) > 0 {
		b64 := base64.StdEncoding.EncodeToString(raw)
		b.WriteString(`<form id="vouchers-import-confirm-form" hx-post="/api/vouchers/import" hx-target="#vouchers-import-result" hx-swap="innerHTML" enctype="multipart/form-data" hx-indicator="#vouchers-import-busy" hx-disabled-elt="find button[type=submit]">`)
		b.WriteString(`<input type="hidden" name="commit" value="1">`)
		fmt.Fprintf(&b, `<input type="hidden" name="file_b64" value="%s">`, b64)
		fmt.Fprintf(&b, `<button class="btn primary" type="submit">%s</button>`, htmlEscape(T("vouchers_import.confirm_btn")))
		b.WriteString(`</form>`)
	} else {
		fmt.Fprintf(&b, `<p class="muted">%s</p>`, htmlEscape(T("vouchers_import.no_valid_rows")))
	}
	b.WriteString(`</div>`)
	_, _ = w.Write(b.Bytes())
}

// renderVoucherImportResult writes the outcome of an actual commit: how
// many vouchers were created (and for how much), plus every rejected row
// (parse-time issues AND commit-time collisions, already merged by the
// caller) with its translated reason.
func renderVoucherImportResult(w http.ResponseWriter, locale string, T func(string) string, imported int, importedTotalMinor int64, allIssues []catimport.VoucherBalanceRowIssue) {
	var b bytes.Buffer
	level, role := "success", "status"
	if imported == 0 {
		level, role = "error", "alert"
	}
	summary := fmt.Sprintf(T("vouchers_import.result_summary"), imported, httpx.FormatMoney(importedTotalMinor, locale))
	fmt.Fprintf(&b, `<div class="pos-notice %s" role="%s"><span class="notice-text">%s</span></div>`, level, role, htmlEscape(summary))
	writeVoucherIssuesTable(&b, T, allIssues)
	_, _ = w.Write(b.Bytes())
}

// writeVoucherIssuesTable renders the shared rejected-rows table both the
// preview and the post-commit result use.
func writeVoucherIssuesTable(b *bytes.Buffer, T func(string) string, issues []catimport.VoucherBalanceRowIssue) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintf(b, `<p class="muted">%s</p>`, htmlEscape(fmt.Sprintf(T("vouchers_import.rejected_summary"), len(issues))))
	b.WriteString(`<table class="table"><thead><tr><th>` + T("vouchers_import.col.code") + `</th><th>` +
		T("vouchers_import.col.reason") + `</th></tr></thead><tbody>`)
	for _, iss := range issues {
		code := iss.Code
		if code == "" {
			code = "—"
		}
		fmt.Fprintf(b, `<tr class="muted"><td>%s</td><td>%s</td></tr>`, htmlEscape(code), htmlEscape(translateVoucherBalanceIssue(T, iss.Reason)))
	}
	b.WriteString(`</tbody></table>`)
}

// translateVoucherBalanceIssue mirrors translateImportIssue (import_page.go)
// for this page's own, smaller reason-code set — catimport has no locale of
// its own (see catimport.go's Issue* doc comment), so this is the one place
// a VoucherBalanceIssue*/voucherImportIssueCodeExists reason code becomes
// operator-facing text.
func translateVoucherBalanceIssue(T func(string) string, reason string) string {
	switch reason {
	case catimport.VoucherBalanceIssueMissingCode:
		return T("vouchers_import.status.missing_code")
	case catimport.VoucherBalanceIssueBadBalance:
		return T("vouchers_import.status.bad_balance")
	case catimport.VoucherBalanceIssueZeroOrNegBalance:
		return T("vouchers_import.status.non_positive_balance")
	case catimport.VoucherBalanceIssueDuplicateCodeInFile:
		return T("vouchers_import.status.duplicate_code_in_file")
	case voucherImportIssueCodeExists:
		return T("vouchers_import.status.code_exists")
	default:
		// A reason code with no case here must never put machine text on
		// the operator's screen (same rule translateImportIssue's own
		// default case documents).
		logging.L().Warnf("[vouchers-import] unrecognised issue reason code %q", reason)
		return T("vouchers_import.status.unknown_issue")
	}
}
