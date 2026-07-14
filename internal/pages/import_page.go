package pages

import (
	"fmt"
	"net/http"
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

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
