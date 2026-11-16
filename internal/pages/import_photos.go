package pages

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/iconid"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/photoimport"
)

// Photo bulk import (ut-docs#6527): CSV of sku + image (folder path, ZIP
// entry or https URL) -> preview -> apply. Existing items' photos only.
//
// Gates: catalog_management (a cashier is refused server-side, #3079) and
// the main till (item photos follow to joined tills from the main till
// only, and a joined till's own upload stays local). Preview and apply run
// the same photoimport.Run; apply adds Commit. One run at a time.

const (
	photoUploadMax   = 600 << 20 // whole multipart body: CSV + ZIP
	photoMemMax      = 8 << 20   // multipart memory before spooling to disk
	photoReportMax   = 8         // failed-rows reports kept in memory
	photoReportTTL   = time.Hour
	photoRowsShown   = 200
	photoFailedRoute = "/api/import/photos/failed.csv"
)

type photoReport struct {
	rep     *photoimport.Report
	owner   string
	created time.Time
}

// photoCatalog adapts the catalogue repository to photoimport.Catalog.
type photoCatalog struct{ repo *data.CatalogRepo }

func (c photoCatalog) ItemsBySKU(ctx context.Context, sku string) ([]string, bool, error) {
	return c.repo.ItemsBySKU(ctx, sku)
}

// HasPhoto: a real thumbnail row, not a library tile.
func (c photoCatalog) HasPhoto(ctx context.Context, itemID string) (bool, error) {
	p, err := c.repo.ItemThumbnailFor(ctx, itemID)
	if err != nil {
		return false, err
	}
	return p != "" && iconid.IDForAssetPath(p) == "", nil
}

func registerImportPhotos(mux *http.ServeMux, d *common.Deps) {
	catRepo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	var fetcher photoimport.URLFetcher = photoimport.NewFetcher()
	var (
		runMu   sync.Mutex // one run at a time
		repMu   sync.Mutex
		reports = map[string]photoReport{}
	)

	notice := func(w http.ResponseWriter, status int, locale, key string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		httpx.RenderNotice(w, locale, "error", key)
	}

	mux.HandleFunc("POST /api/import/photos", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "catalog_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		locale := httpx.ResolveLocale(w, r)
		if d.SyncPrimaryURL(r.Context()) != "" {
			notice(w, http.StatusConflict, locale, "photoimport.error.main_till_only")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, photoUploadMax)
		if err := r.ParseMultipartForm(photoMemMax); err != nil {
			notice(w, http.StatusRequestEntityTooLarge, locale, "photoimport.error.too_large")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		var rows []photoimport.Row
		csvFile, _, csvErr := r.FormFile("file")
		if csvErr == nil {
			defer csvFile.Close()
			raw, err := io.ReadAll(io.LimitReader(csvFile, photoimport.MaxCSVBytes+1))
			if err != nil {
				notice(w, http.StatusBadRequest, locale, "photoimport.error.bad_csv")
				return
			}
			rows, err = photoimport.ParseCSV(raw)
			switch {
			case errors.Is(err, photoimport.ErrNoSKUColumn):
				notice(w, http.StatusBadRequest, locale, "photoimport.error.no_sku_column")
				return
			case errors.Is(err, photoimport.ErrCSVTooLarge):
				notice(w, http.StatusRequestEntityTooLarge, locale, "photoimport.error.too_large")
				return
			case err != nil:
				notice(w, http.StatusBadRequest, locale, "photoimport.error.bad_csv")
				return
			}
		}
		var zr *photoimport.Zip
		if zf, zh, err := r.FormFile("zip"); err == nil {
			defer zf.Close()
			zr, err = photoimport.OpenZip(zf, zh.Size)
			if err != nil {
				notice(w, http.StatusBadRequest, locale, "photoimport.error.bad_zip")
				return
			}
			if csvErr != nil {
				rows = photoimport.RowsFromZip(zr) // ZIP alone: file name = SKU
				if len(rows) > photoimport.MaxRows {
					notice(w, http.StatusRequestEntityTooLarge, locale, "photoimport.error.too_large")
					return
				}
			}
		}
		if csvErr != nil && zr == nil {
			notice(w, http.StatusBadRequest, locale, "photoimport.error.no_input")
			return
		}
		folder := strings.TrimSpace(r.FormValue("folder"))
		if folder != "" {
			// Absolute only: a relative folder would resolve against the
			// process's working directory, not the shop's folder.
			if st, err := os.Stat(folder); !filepath.IsAbs(folder) || err != nil || !st.IsDir() {
				notice(w, http.StatusBadRequest, locale, "photoimport.error.folder")
				return
			}
		}
		if !runMu.TryLock() {
			notice(w, http.StatusConflict, locale, "photoimport.error.busy")
			return
		}
		defer runMu.Unlock()

		commit := r.FormValue("commit") == "1"
		stored := 0
		opts := photoimport.Options{
			Folder:  folder,
			Zip:     zr,
			Replace: r.FormValue("replace") == "1",
			Commit:  commit,
			Catalog: photoCatalog{catRepo},
			Fetcher: fetcher,
			Store: func(ctx context.Context, itemID string, img image.Image) error {
				if err := catalog.StoreItemPhoto(ctx, catRepo, itemID, img); err != nil {
					if !errors.Is(err, catalog.ErrItemThumbRow) {
						return err
					}
					log.Printf("[photoimport] %v", err)
				}
				catalog.RefreshBasketPhoto(ctx, d, catRepo, itemID)
				stored++
				return nil
			},
		}
		rep, err := photoimport.Run(r.Context(), rows, opts)
		if commit && stored > 0 {
			// Photos are files on /api/sync/assets: tell linked tills to pull.
			d.NudgeLink(fleetlink.ScopeAdmin)
		}
		if commit {
			payload := map[string]any{"photos_saved": stored, "replace": opts.Replace, "rows": len(rows), "counts": rep.Counts}
			if aerr := posRepo.InsertAudit(context.WithoutCancel(r.Context()), nil, getSessionUserID(r), "catalog", "-", "photo_import",
				payload, time.Now().UTC().Format(time.RFC3339), ""); aerr != nil {
				log.Printf("[photoimport] audit: %v", aerr)
			}
		}
		if err != nil {
			// Interrupted (client gone) or a database error: what was done
			// before is saved and a re-run skips it (replace off).
			log.Printf("[photoimport] run stopped: %v", err)
			notice(w, http.StatusInternalServerError, locale, "photoimport.error.interrupted")
			return
		}

		id := ""
		if commit && hasFailures(rep) {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
			repMu.Lock()
			now := time.Now()
			for k, v := range reports {
				if now.Sub(v.created) > photoReportTTL {
					delete(reports, k)
				}
			}
			for len(reports) >= photoReportMax {
				oldest, ot := "", now
				for k, v := range reports {
					if v.created.Before(ot) {
						oldest, ot = k, v.created
					}
				}
				delete(reports, oldest)
			}
			reports[id] = photoReport{rep: rep, owner: getSessionUserID(r), created: now}
			repMu.Unlock()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writePhotoReport(w, locale, rep, id)
	})

	mux.HandleFunc("GET /api/import/photos/failed.csv", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "catalog_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		repMu.Lock()
		pr, ok := reports[r.URL.Query().Get("id")]
		repMu.Unlock()
		if !ok || pr.owner != getSessionUserID(r) || time.Since(pr.created) > photoReportTTL {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="photo-import-failed.csv"`)
		w.Header().Set("Cache-Control", "no-store")
		_ = pr.rep.WriteFailedCSV(w)
	})
}

func hasFailures(rep *photoimport.Report) bool {
	for s, n := range rep.Counts {
		if n > 0 && photoimport.IsFailure(s) {
			return true
		}
	}
	return false
}

var photoStatusOrder = []string{
	photoimport.StatusAdd, photoimport.StatusReplace, photoimport.StatusSkipHasPhoto,
	photoimport.StatusUnknownSKU, photoimport.StatusAmbiguousSKU, photoimport.StatusDuplicateSKU,
	photoimport.StatusVariantSKU, photoimport.StatusNoSource, photoimport.StatusOutside,
	photoimport.StatusNotFound, photoimport.StatusNotRegular, photoimport.StatusTooBig,
	photoimport.StatusNotImage, photoimport.StatusImageTooBig, photoimport.StatusFetchFailed,
	photoimport.StatusBlockedURL, photoimport.StatusStoreFailed, photoimport.StatusTotalLimit,
}

// photoStatusKeys maps a status code to its locale key (literal keys so the
// i18n guards see them).
var photoStatusKeys = map[string]string{
	photoimport.StatusAdd:          "photoimport.status.add",
	photoimport.StatusReplace:      "photoimport.status.replace",
	photoimport.StatusSkipHasPhoto: "photoimport.status.skipped_has_photo",
	photoimport.StatusUnknownSKU:   "photoimport.status.unknown_sku",
	photoimport.StatusAmbiguousSKU: "photoimport.status.ambiguous_sku",
	photoimport.StatusDuplicateSKU: "photoimport.status.duplicate_sku",
	photoimport.StatusVariantSKU:   "photoimport.status.variant_sku",
	photoimport.StatusNoSource:     "photoimport.status.no_source",
	photoimport.StatusOutside:      "photoimport.status.outside_folder",
	photoimport.StatusNotFound:     "photoimport.status.not_found",
	photoimport.StatusNotRegular:   "photoimport.status.not_regular",
	photoimport.StatusTooBig:       "photoimport.status.too_big",
	photoimport.StatusNotImage:     "photoimport.status.not_image",
	photoimport.StatusImageTooBig:  "photoimport.status.image_too_large",
	photoimport.StatusFetchFailed:  "photoimport.status.fetch_failed",
	photoimport.StatusBlockedURL:   "photoimport.status.blocked_url",
	photoimport.StatusStoreFailed:  "photoimport.status.store_failed",
	photoimport.StatusTotalLimit:   "photoimport.status.total_limit",
}

func writePhotoReport(w io.Writer, locale string, rep *photoimport.Report, failedID string) {
	T := func(k string) string { return httpx.T(locale, k) }
	ok := rep.Counts[photoimport.StatusAdd] + rep.Counts[photoimport.StatusReplace]
	summaryKey := "photoimport.summary.preview"
	if rep.Committed {
		summaryKey = "photoimport.summary.done"
	}
	fmt.Fprintf(w, `<div class="pos-notice success" role="status" data-photo-import-summary="%s"><span class="notice-text">%s</span></div>`,
		map[bool]string{true: "done", false: "preview"}[rep.Committed], htmlEscape(fmt.Sprintf(T(summaryKey), ok)))
	fmt.Fprint(w, `<ul class="photo-import-counts">`)
	for _, s := range photoStatusOrder {
		if n := rep.Counts[s]; n > 0 {
			fmt.Fprintf(w, `<li data-photo-status="%s">%s: %d</li>`, s, htmlEscape(T(photoStatusKeys[s])), n)
		}
	}
	fmt.Fprint(w, `</ul>`)
	if failedID != "" {
		fmt.Fprintf(w, `<p><a class="btn secondary" href="%s?id=%s" download>%s</a></p>`, photoFailedRoute, failedID, htmlEscape(T("photoimport.failed_csv")))
	}
	shown := 0
	fmt.Fprintf(w, `<table class="table photo-import-rows"><thead><tr><th>%s</th><th>%s</th><th>%s</th></tr></thead><tbody>`,
		htmlEscape(T("photoimport.col.line")), htmlEscape(T("photoimport.col.sku")), htmlEscape(T("photoimport.col.result")))
	for _, row := range rep.Rows {
		if shown >= photoRowsShown {
			break
		}
		shown++
		fmt.Fprintf(w, `<tr data-photo-status="%s"><td>%d</td><td>%s</td><td>%s</td></tr>`,
			row.Status, row.Line, htmlEscape(row.SKU), htmlEscape(T(photoStatusKeys[row.Status])))
	}
	fmt.Fprint(w, `</tbody></table>`)
	if len(rep.Rows) > shown {
		fmt.Fprintf(w, `<p class="muted">%s</p>`, htmlEscape(fmt.Sprintf(T("photoimport.truncated"), shown)))
	}
}
