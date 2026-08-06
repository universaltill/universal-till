package pages

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// Catalog import (docs: architecture/catalog-import.md, G22a): upload a
// Loyverse/Square/generic CSV export → preview → import. Preview writes
// nothing; import is idempotent (existing barcode/SKU rows are skipped).
func registerImport(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("GET /import", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Redirect(w, r, "/catalog", http.StatusSeeOther)
			return
		}
		httpx.Render("ui/pages/import.html", map[string]any{
			"title":     "Import",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
		})(w, r)
	})

	// G22b — catalog export. The CSV round-trips with our own importer
	// (column names come from its synonym table), so "export → import on a
	// fresh till" is a supported migration path, not an accident.
	writeCatalogCSV := func(out io.Writer, rows []data.ExportRow, decimals int) {
		cw := csv.NewWriter(out)
		_ = cw.Write([]string{"Name", "SKU", "Barcode", "Price", "Category",
			"Description", "Sold by weight", "In stock", "Active"})
		yn := func(b bool) string {
			if b {
				return "Y"
			}
			return "N"
		}
		for _, e := range rows {
			_ = cw.Write([]string{
				e.Name, e.SKU, e.Barcode, minorToDecimal(e.PriceMinor, decimals),
				e.Category, e.Description, yn(e.IsWeighed),
				strconv.FormatFloat(e.Stock, 'f', -1, 64), yn(e.IsActive),
			})
		}
		cw.Flush()
	}

	mux.HandleFunc("GET /api/catalog/export", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		rows, err := repo.ExportRows(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="catalog-`+time.Now().UTC().Format("2006-01-02")+`.csv"`)
		writeCatalogCSV(w, rows, httpx.ActiveCurrency().Decimals)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "export",
			map[string]any{"rows": len(rows)}, time.Now().UTC().Format(time.RFC3339), "")
	})

	// Save-to-Downloads: the direct download above relies on the browser
	// handling a Content-Disposition attachment, which the desktop WebView does
	// NOT — the link silently did nothing there. This POST writes the CSV into
	// the user's Downloads folder (htmx, works in the app and in a browser).
	mux.HandleFunc("POST /api/catalog/export-save", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isManagerOrAuthOff(r) {
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "settings.enrol.forbidden"))
			return
		}
		rows, err := repo.ExportRows(r.Context())
		if err != nil {
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "import.export_save_failed"))
			return
		}
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "import.export_save_failed"))
			return
		}
		dstDir := filepath.Join(home, "Downloads")
		_ = os.MkdirAll(dstDir, 0o755)
		dst := filepath.Join(dstDir, "catalog-"+time.Now().UTC().Format("2006-01-02")+".csv")
		f, ferr := os.Create(dst)
		if ferr != nil {
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "import.export_save_failed"))
			return
		}
		writeCatalogCSV(f, rows, httpx.ActiveCurrency().Decimals)
		_ = f.Close()
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "export",
			map[string]any{"rows": len(rows), "dest": dst}, time.Now().UTC().Format(time.RFC3339), "")
		fmt.Fprintf(w, `<span>✓ %s <code>%s</code></span>`, httpx.T(locale, "settings.backup.saved_to"), dst)
	})

	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager or admin required", http.StatusForbidden)
			return
		}
		if err := r.ParseMultipartForm(20 << 20); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "csv file required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		commit := r.FormValue("commit") == "1"

		res, err := catimport.Parse(file, httpx.ActiveCurrency().Decimals)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Annotate duplicates (server truth) for both preview and commit.
		type rowView struct {
			catimport.ImportItem
			Status string // ok | skip reason
			Warned bool   // true once a commit-time warning was appended to Status
		}
		var rows []rowView
		importable := 0
		for _, it := range res.Items {
			status := "ok"
			switch {
			case it.Issue != "":
				status = it.Issue
			case it.Barcode != "":
				if exists, _ := repo.BarcodeExists(r.Context(), it.Barcode); exists {
					status = "barcode already in catalog"
				}
			}
			if status == "ok" && it.SKU != "" {
				if exists, _ := repo.SKUExists(r.Context(), it.SKU); exists {
					status = "SKU already in catalog"
				}
			}
			if status == "ok" {
				importable++
			}
			rows = append(rows, rowView{ImportItem: it, Status: status})
		}

		created, warned, failed := 0, 0, 0
		if commit {
			// Opening stock from the source file lands as a "receive"
			// movement at the default location (same path as the
			// inventory page), so the migration carries quantities too.
			locID, locErr := posRepo.EnsureStockLocation(r.Context())
			for i := range rows {
				if rows[i].Status != "ok" {
					continue
				}
				it := rows[i].ImportItem
				// Departments are top-level categories; the item's category
				// nests under its department (docs/arch/enterprise-department-
				// stores.md). With no department the category stays top-level;
				// with a department but no category the item sits directly in
				// the department. Parent-scoped ensure keeps this idempotent.
				var catID *string
				deptID := ""
				if it.Department != "" {
					id, err := repo.EnsureCategoryUnder(r.Context(), it.Department, "")
					if err != nil {
						rows[i].Status = "failed: department: " + err.Error()
						failed++
						continue
					}
					deptID = id
				}
				if it.Category != "" {
					id, err := repo.EnsureCategoryUnder(r.Context(), it.Category, deptID)
					if err != nil {
						rows[i].Status = "failed: category: " + err.Error()
						failed++
						continue
					}
					catID = &id
				} else if deptID != "" {
					catID = &deptID
				}
				// The item, its inventory row, and its opening-stock movement
				// land together in one transaction (ut-docs#310): the only
				// new failure path this introduces is tx.Commit() itself
				// erroring below — a genuine unexpected DB-level failure —
				// which rolls the whole row back instead of leaving a
				// partially-built item, exactly like a mid-row crash always
				// implicitly would have. Barcode attach deliberately stays
				// OUTSIDE this transaction, attempted only after it commits:
				// AddBarcode owns its own #304 BEGIN IMMEDIATE transaction on
				// a separate connection, which can't see this transaction's
				// writes until they're committed, and folding it in would
				// mean re-implementing #304's race protection here (see the
				// card's own discussion of why that's out of scope).
				tx, err := d.Db.BeginTx(r.Context(), nil)
				if err != nil {
					rows[i].Status = "failed: " + err.Error()
					failed++
					continue
				}
				itemID, err := repo.CreateItemTx(r.Context(), tx, pos.ItemInput{
					Name: it.Name, SKU: it.SKU, BasePrice: it.PriceMinor,
					Description: it.Description, CategoryID: catID,
					Unit: "each", IsWeighed: it.IsWeighed, IsActive: true,
				})
				if err != nil {
					_ = tx.Rollback()
					rows[i].Status = "failed: " + err.Error()
					failed++
					continue
				}
				// stockWarning/stockRecorded are decided now (inside the
				// transaction) but only turned into row status / a published
				// event once tx.Commit() below actually lands them — same
				// warn-and-continue outcome as before this card for every
				// case already covered by TestImport_LocationLookupFailure-
				// WarnsStockNotCarried, TestImport_NegativeStockQuantityWarns
				// and TestImport_StockRecordingFailureWarnsAndDoesNotPublish:
				// a stock-recording failure — DB-level or not — still just
				// warns on an otherwise-committed row, never fails it.
				var stockWarning string
				stockRecorded := false
				switch {
				case it.HasStock && it.Stock < 0:
					// catimport parses a negative quantity happily and it is
					// then dropped by the `> 0` test below with no trace —
					// warn instead of discarding it in silence.
					stockWarning = fmt.Sprintf(
						"stock not carried: negative quantity %g in source file", it.Stock)
				case it.HasStock && it.Stock > 0 && locErr != nil:
					// EnsureStockLocation ran once, outside the loop; when it
					// failed, every row with stock would otherwise lose it
					// silently while still reading "created" — the exact bug
					// this card fixes, one branch over.
					stockWarning = "stock not carried: " + locErr.Error()
				case it.HasStock && it.Stock > 0:
					// Savepoint-scoped, not the plain tx-accepting
					// RecordStockMovement: a failure on any of its four
					// statements must only discard the stock movement
					// itself, never the item + inventory row already
					// written earlier in this same transaction (a stock-
					// recording failure is warn-and-continue here, same as
					// every other case in this switch — see
					// TestImport_StockRecordingFailureWarnsAndDoesNotPublish
					// — not a reason to fail the whole row).
					if _, err := posRepo.RecordStockMovementSavepoint(r.Context(), tx, pos.StockMovementInput{
						ItemID: itemID, LocationID: locID, Type: "receive",
						Quantity: it.Stock, Reason: "catalog import",
						ActorID: getSessionUserID(r),
					}); err != nil {
						stockWarning = "stock not carried: " + err.Error()
					} else {
						stockRecorded = true
					}
				}
				if err := tx.Commit(); err != nil {
					rows[i].Status = "failed: " + err.Error()
					failed++
					continue
				}
				// Row-level warnings accumulate rather than short-circuit —
				// a barcode attach failure must not also skip the
				// stock-import outcome below it (ut-docs#293) — both the
				// reason and the stock outcome need to survive into the
				// row's status, so neither branch clobbers the other's
				// message.
				var warnings []string
				if it.Barcode != "" {
					if err := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
						Barcode: it.Barcode, ItemID: itemID, IsPrimary: true,
					}); err != nil {
						warnings = append(warnings, "barcode attach failed: "+err.Error())
					}
				} else if it.BarcodeIssue != "" {
					// The CSV carried a barcode value, but the importer's
					// normalizeBarcode discarded it (unsupported shape, e.g.
					// a 4-digit PLU) — the item still imports, but silently
					// dropping the barcode with no trace is the same defect
					// class this card exists to fix (ut-docs#293).
					warnings = append(warnings, it.BarcodeIssue)
				}
				if stockWarning != "" {
					warnings = append(warnings, stockWarning)
				}
				if stockRecorded {
					// Mirror imported stock to inventory connectors
					// (best-effort, non-blocking) — only once the movement
					// actually committed, not merely once it was attempted.
					publishStockAdjusted(r.Context(), d, plugins.StockAdjustedEvent{
						ItemID:   itemID,
						SKU:      it.SKU,
						DeltaQty: it.Stock,
						Reason:   "received",
						Location: locID,
					})
				}
				if len(warnings) > 0 {
					rows[i].Status = "created; " + strings.Join(warnings, "; ")
					rows[i].Warned = true
					warned++
				} else {
					rows[i].Status = "created"
				}
				created++
			}
			_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "import",
				map[string]any{"format": res.Format, "created": created, "warned": warned, "failed": failed, "rows": len(rows)},
				time.Now().UTC().Format(time.RFC3339), "")
		}

		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)
		var b strings.Builder
		if commit {
			fmt.Fprintf(&b, `<p><strong>✓ %s: %d — %s: %d — %s: %d</strong></p>`,
				T("import.created"), created, T("import.warned"), warned,
				T("import.skipped"), len(rows)-created)
		} else {
			fmt.Fprintf(&b, `<p><strong>%s: %s · %d %s, %d %s</strong></p>`,
				T("import.detected"), res.Format, importable, T("import.ready"), len(rows)-importable, T("import.with_issues"))
		}
		b.WriteString(`<table class="table"><thead><tr><th>` + T("catalog.col.name") + `</th><th>` +
			T("catalog.col.price") + `</th><th>` + T("catalog.barcode") + `</th><th>` +
			T("catalog.category") + `</th><th>` + T("import.status") + `</th></tr></thead><tbody>`)
		// Warned rows must survive the 200-row display cap — an operator who
		// only gets to see the first 200 of a large import must not lose the
		// rows that actually need their attention (ut-docs#293 review).
		// Partition warned rows to the front, unconditionally rendered, and
		// apply the cap only to the remaining (non-warned) rows so the
		// "… N more" count stays accurate for what's actually still hidden.
		var warnedRows, plainRows []rowView
		for _, row := range rows {
			if row.Warned {
				warnedRows = append(warnedRows, row)
			} else {
				plainRows = append(plainRows, row)
			}
		}
		writeRow := func(row rowView) {
			cls := ""
			if row.Status != "ok" && row.Status != "created" {
				cls = ` class="muted"`
			}
			fmt.Fprintf(&b, `<tr%s><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				cls, htmlEscape(row.Name), httpx.FormatMoney(row.PriceMinor, locale),
				htmlEscape(row.Barcode), htmlEscape(row.Category), htmlEscape(row.Status))
		}
		for _, row := range warnedRows {
			writeRow(row)
		}
		plainShown := 0
		for _, row := range plainRows {
			if plainShown >= 200 {
				break
			}
			writeRow(row)
			plainShown++
		}
		if plainShown < len(plainRows) {
			fmt.Fprintf(&b, `<tr><td colspan="5" class="muted">… %d more</td></tr>`, len(plainRows)-plainShown)
		}
		b.WriteString(`</tbody></table>`)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
	})
}

// minorToDecimal renders minor units as a plain decimal ("1.20") — the
// exact shape the importer's price parser reads back.
func minorToDecimal(minor int64, decimals int) string {
	if decimals <= 0 {
		return strconv.FormatInt(minor, 10)
	}
	div := int64(1)
	for range decimals {
		div *= 10
	}
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%0*d", sign, minor/div, decimals, minor%div)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
