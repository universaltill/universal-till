package pages

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
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
		decimals := httpx.ActiveCurrency().Decimals
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="catalog-`+time.Now().UTC().Format("2006-01-02")+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"Name", "SKU", "Barcode", "Price", "Category",
			"Description", "Sold by weight", "In stock", "Active"})
		for _, e := range rows {
			yn := func(b bool) string {
				if b {
					return "Y"
				}
				return "N"
			}
			_ = cw.Write([]string{
				e.Name, e.SKU, e.Barcode, minorToDecimal(e.PriceMinor, decimals),
				e.Category, e.Description, yn(e.IsWeighed),
				strconv.FormatFloat(e.Stock, 'f', -1, 64), yn(e.IsActive),
			})
		}
		cw.Flush()
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "export",
			map[string]any{"rows": len(rows)}, time.Now().UTC().Format(time.RFC3339), "")
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

		created, failed := 0, 0
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
				var catID *string
				if id, err := repo.EnsureCategory(r.Context(), it.Category); err != nil {
					rows[i].Status = "failed: category: " + err.Error()
					failed++
					continue
				} else if id != "" {
					catID = &id
				}
				itemID, err := pos.CreateItem(r.Context(), d.Db, pos.ItemInput{
					Name: it.Name, SKU: it.SKU, BasePrice: it.PriceMinor,
					Description: it.Description, CategoryID: catID,
					Unit: "each", IsWeighed: it.IsWeighed, IsActive: true,
				})
				if err != nil {
					rows[i].Status = "failed: " + err.Error()
					failed++
					continue
				}
				if it.Barcode != "" {
					if err := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
						Barcode: it.Barcode, ItemID: itemID, IsPrimary: true,
					}); err != nil {
						rows[i].Status = "created; barcode attach failed"
						created++
						continue
					}
				}
				if it.HasStock && it.Stock > 0 && locErr == nil {
					if _, err := pos.RecordStockMovement(r.Context(), d.Db, pos.StockMovementInput{
						ItemID: itemID, LocationID: locID, Type: "receive",
						Quantity: it.Stock, Reason: "catalog import",
						ActorID: getSessionUserID(r),
					}); err != nil {
						rows[i].Status = "created; stock not carried: " + err.Error()
						created++
						continue
					}
				}
				rows[i].Status = "created"
				created++
			}
			_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "import",
				map[string]any{"format": res.Format, "created": created, "failed": failed, "rows": len(rows)},
				time.Now().UTC().Format(time.RFC3339), "")
		}

		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)
		var b strings.Builder
		if commit {
			fmt.Fprintf(&b, `<p><strong>✓ %s: %d — %s: %d</strong></p>`,
				T("import.created"), created, T("import.skipped"), len(rows)-created)
		} else {
			fmt.Fprintf(&b, `<p><strong>%s: %s · %d %s, %d %s</strong></p>`,
				T("import.detected"), res.Format, importable, T("import.ready"), len(rows)-importable, T("import.with_issues"))
		}
		b.WriteString(`<table class="table"><thead><tr><th>` + T("catalog.col.name") + `</th><th>` +
			T("catalog.col.price") + `</th><th>` + T("catalog.barcode") + `</th><th>` +
			T("catalog.category") + `</th><th>` + T("import.status") + `</th></tr></thead><tbody>`)
		shown := 0
		for _, row := range rows {
			if shown >= 200 {
				fmt.Fprintf(&b, `<tr><td colspan="5" class="muted">… %d more</td></tr>`, len(rows)-shown)
				break
			}
			shown++
			cls := ""
			if row.Status != "ok" && row.Status != "created" {
				cls = ` class="muted"`
			}
			fmt.Fprintf(&b, `<tr%s><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				cls, htmlEscape(row.Name), httpx.FormatMoney(row.PriceMinor, locale),
				htmlEscape(row.Barcode), htmlEscape(row.Category), htmlEscape(row.Status))
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
