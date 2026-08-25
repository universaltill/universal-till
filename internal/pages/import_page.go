package pages

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
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
	"github.com/universaltill/universal-till/internal/taxrate"
)

// Catalog import (docs: architecture/catalog-import.md, G22a): upload a
// Loyverse/Square/generic CSV export → preview → import. Preview writes
// nothing; import is idempotent (existing barcode/SKU rows are skipped).
func registerImport(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	settingsRepo := data.NewSettingsRepo(d.Db)

	mux.HandleFunc("GET /import", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "import_export") {
			http.Redirect(w, r, "/catalog", http.StatusSeeOther)
			return
		}
		httpx.Render("ui/pages/import.html", map[string]any{
			"title":     "Import",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
		})(w, r)
	})

	mux.HandleFunc("GET /api/catalog/export", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "import_export") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		rows, err := repo.ExportRows(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "import.export_save_failed", "import_export", err)
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
		if !canPerform(d, r, "import_export") {
			httpx.RenderNotice(w, locale, "error", "settings.enrol.forbidden")
			return
		}
		rows, err := repo.ExportRows(r.Context())
		if err != nil {
			httpx.RenderNotice(w, locale, "error", "import.export_save_failed")
			return
		}
		home, herr := os.UserHomeDir()
		if herr != nil {
			httpx.RenderNotice(w, locale, "error", "import.export_save_failed")
			return
		}
		dstDir := filepath.Join(home, "Downloads")
		_ = os.MkdirAll(dstDir, 0o755)
		dst := filepath.Join(dstDir, "catalog-"+time.Now().UTC().Format("2006-01-02")+".csv")
		f, ferr := os.Create(dst)
		if ferr != nil {
			httpx.RenderNotice(w, locale, "error", "import.export_save_failed")
			return
		}
		writeCatalogCSV(f, rows, httpx.ActiveCurrency().Decimals)
		_ = f.Close()
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "export",
			map[string]any{"rows": len(rows), "dest": dst}, time.Now().UTC().Format(time.RFC3339), "")
		httpx.RenderNotice(w, locale, "success", "settings.backup.saved_to", "<code>"+dst+"</code>")
	})

	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "import_export") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if err := r.ParseMultipartForm(20 << 20); err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "csv file required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		commit := r.FormValue("commit") == "1"

		// Resolved up front (ut-docs#303): every row status below is a
		// locale key, not English prose, so T needs to be live before the
		// preview loop builds the first one, and before the Parse error
		// below (the first thing an operator sees on a wrong-format file).
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)

		// Format auto-detection (ut-docs#511): sniff the ZIP local-file-
		// header magic before choosing a parser — a speedy kasse / pepperm
		// cashbox .bkp is a plain ZIP, everything else on this page is a
		// CSV. No separate page/route; the operator just uploads either.
		isBkp, sniffErr := sniffZipUpload(file)
		if sniffErr != nil {
			log.Printf("[import] sniff upload: %v", sniffErr)
			http.Error(w, T("import.error.invalid_file"), http.StatusBadRequest)
			return
		}

		var res catimport.Result
		if isBkp {
			res, err = catimport.ParseBkp(file, header.Size, httpx.ActiveCurrency().Decimals)
		} else {
			// Same shared registry matcher AddBarcode/the scan path use
			// (ADR-0059 Decision §3, ut-docs#936) — never brick a CSV
			// upload on a settings read failure, same rule as AddBarcode:
			// EnabledBarcodeSymbologies already returns the compatibility-
			// preserving default set alongside any error.
			enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
			if symErr != nil {
				log.Printf("[import] enabled symbologies unavailable, using defaults: %v", symErr)
			}
			res, err = catimport.Parse(file, httpx.ActiveCurrency().Decimals, enabledIDs)
		}
		if err != nil {
			switch {
			case errors.Is(err, catimport.ErrNoNameColumn):
				http.Error(w, T("import.error.no_name_column"), http.StatusBadRequest)
				return
			case errors.Is(err, catimport.ErrBkpMissingFiles), errors.Is(err, catimport.ErrBkpInvalidMeta), errors.Is(err, catimport.ErrBkpTooLarge):
				// The upload looked like a ZIP but isn't a recognisable
				// .bkp backup — log the real detail, never raw text on the
				// operator's screen (ut-docs#303's rule, same as below).
				log.Printf("[import] bkp parse: %v", err)
				http.Error(w, T("import.error.bkp_unrecognised"), http.StatusBadRequest)
				return
			case errors.Is(err, catimport.ErrBkpChecksumMismatch):
				log.Printf("[import] bkp parse: %v", err)
				http.Error(w, T("import.error.bkp_checksum_failed"), http.StatusBadRequest)
				return
			}
			// A rarer, lower-level parse failure (malformed CSV row,
			// unreadable header, corrupt zip entry) — same rule as
			// everywhere else in this handler: log the real detail, never
			// put raw Go/csv/zip error text on the operator's screen
			// (ut-docs#303).
			log.Printf("[import] parse: %v", err)
			http.Error(w, T("import.error.invalid_file"), http.StatusBadRequest)
			return
		}

		// ut-docs#970: a fresh till defaults to GBP with nothing recording
		// whether an operator ever actually chose that — so a catalogue
		// import (e.g. a German backup) can silently price every row under
		// a currency nobody confirmed. Gate the WRITE, not the parse/preview
		// (preview writes nothing, so it's never gated — see the warning
		// line below instead).
		// ut-docs#970 review (F11): defaults to unconfirmed, not confirmed —
		// a Settings.Get error must fail SAFE (prompt) for a money-labelling
		// gate, not silently let the commit through under an unverified
		// currency because the read happened to fail.
		currencyConfirmed := false
		justConfirmedCurrency := false // this request is the one that supplied confirm_currency, for the audit log
		if confirmedVal, _, cerr := d.Settings.Get(r.Context(), common.KeyCurrencyConfirmed); cerr != nil {
			log.Printf("[import] read currency confirmed flag: %v", cerr)
		} else {
			currencyConfirmed = confirmedVal == "true"
		}
		if commit && !currencyConfirmed {
			confirmCode := strings.ToUpper(strings.TrimSpace(r.FormValue("confirm_currency")))
			if confirmCode == "" {
				// First attempt to commit with an unconfirmed currency:
				// write nothing, ask instead.
				renderImportCurrencyConfirm(w, T)
				return
			}
			// ut-docs#970 review (F1, blocker): CurrencyByCode(v).Code == v
			// is ALWAYS true for an already-uppercased/trimmed v — it's a
			// permissive lookup with a "fabricate a plausible CurrencyInfo"
			// fallback, not a membership check, so this "validation" never
			// actually rejected anything. Confirmed live: POST'ing
			// confirm_currency=XYZ switched the till to a currency that
			// isn't in the registry at all and marked it confirmed — the
			// exact "money labelled as the wrong currency" class this card
			// exists to fix, reopened through the fix's own confirm step.
			if !httpx.IsKnownCurrency(confirmCode) {
				http.Error(w, T("import.error.invalid_currency"), http.StatusBadRequest)
				return
			}
			chosen := httpx.CurrencyByCode(confirmCode)
			active := httpx.ActiveCurrency()
			if chosen.Code != active.Code {
				// The operator says the file is in a different currency
				// than the till's (unconfirmed) default — switch the till
				// to it AND re-parse the original upload under the new
				// decimal count. Decimals vary per currency (GBP=2, IRT=0,
				// ...); committing the already-parsed rows unchanged would
				// silently keep the WRONG minor-unit amounts even though
				// the currency label was fixed.
				if _, serr := file.Seek(0, io.SeekStart); serr != nil {
					log.Printf("[import] seek upload for re-parse: %v", serr)
					http.Error(w, T("import.error.invalid_file"), http.StatusInternalServerError)
					return
				}
				if isBkp {
					res, err = catimport.ParseBkp(file, header.Size, chosen.Decimals)
				} else {
					enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
					if symErr != nil {
						log.Printf("[import] enabled symbologies unavailable, using defaults: %v", symErr)
					}
					res, err = catimport.Parse(file, chosen.Decimals, enabledIDs)
				}
				if err != nil {
					log.Printf("[import] re-parse under confirmed currency %s: %v", chosen.Code, err)
					http.Error(w, T("import.error.invalid_file"), http.StatusBadRequest)
					return
				}
				st := d.CurrentState()
				fromCurrency := active.Code
				st.Currency = chosen.Code
				if serr := common.SaveState(r.Context(), d.Settings, st); serr != nil {
					log.Printf("[import] save switched currency: %v", serr)
					http.Error(w, T("import.error.invalid_file"), http.StatusInternalServerError)
					return
				}
				d.SetState(st)
				// ut-docs#970 review (F4): this is the same destructive-ish
				// switch /api/settings/save's currency card performs — that
				// path audits it (settingsAudit, "setting_upserted");
				// switching from here must too, and must name what it
				// switched FROM (the combined import-commit audit below only
				// ever records the final currency, never the change).
				if aerr := posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "settings", common.KeyCurrency,
					"currency_switched_by_import", map[string]any{"from": fromCurrency, "to": chosen.Code},
					time.Now().UTC().Format(time.RFC3339), ""); aerr != nil {
					log.Printf("[import] audit currency switch: %v", aerr)
				}
				httpx.InitCurrency(st.Currency)
			}
			if serr := d.Settings.Set(r.Context(), common.KeyCurrencyConfirmed, "true"); serr != nil {
				log.Printf("[import] mark currency confirmed: %v", serr)
				http.Error(w, T("import.error.invalid_file"), http.StatusInternalServerError)
				return
			}
			currencyConfirmed = true
			justConfirmedCurrency = true
		}

		// Annotate duplicates (server truth) for both preview and commit.
		type rowView struct {
			catimport.ImportItem
			Status  string // translated display text
			Skipped bool   // preview-time issue/duplicate — never entered the commit loop as importable
			Warned  bool   // created, but with a warning
			Failed  bool   // commit-time failure (category/department/item creation)
		}
		var rows []rowView
		importable := 0
		for _, it := range res.Items {
			status := T("import.status.ok")
			skipped := false
			switch {
			case it.Issue != "":
				status, skipped = translateImportIssue(T, it), true
			case it.Barcode != "":
				if exists, _ := repo.BarcodeExists(r.Context(), it.Barcode); exists {
					status, skipped = T("import.status.barcode_already_in_catalog"), true
				}
			}
			if !skipped && it.SKU != "" {
				if exists, _ := repo.SKUExists(r.Context(), it.SKU); exists {
					status, skipped = T("import.status.sku_already_in_catalog"), true
				}
			}
			if !skipped {
				importable++
			}
			rows = append(rows, rowView{ImportItem: it, Status: status, Skipped: skipped})
		}

		created, warned, failed := 0, 0, 0
		taxCodesCreated := 0
		// Genuine (dine-in ≠ takeaway) override pairs discovered during this
		// commit, tax_code_id → takeaway basis points — fed into
		// ut-plugin-tax-de's takeaway_rate_overrides setting after the loop
		// (ut-docs#512), regardless of whether the tax code row itself was
		// created just now or already existed (an existing row's pairing may
		// not be in the plugin's setting yet).
		takeawayOverrides := map[string]int{}
		overridesSet := 0
		overridesFailed := false
		overridesPluginDisabled := false
		if commit {
			// Opening stock from the source file lands as a "receive"
			// movement at the default location (same path as the
			// inventory page), so the migration carries quantities too.
			locID, locErr := posRepo.EnsureStockLocation(r.Context())
			for i := range rows {
				if rows[i].Skipped {
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
						// Raw DB error text (table/constraint names) must
						// never reach the operator's screen — log it, show
						// a generic translated reason (ut-docs#303).
						log.Printf("[import] ensure department %q: %v", it.Department, err)
						rows[i].Status = T("import.status.department_failed")
						rows[i].Failed = true
						failed++
						continue
					}
					deptID = id
				}
				if it.Category != "" {
					id, err := repo.EnsureCategoryUnder(r.Context(), it.Category, deptID)
					if err != nil {
						log.Printf("[import] ensure category %q: %v", it.Category, err)
						rows[i].Status = T("import.status.category_failed")
						rows[i].Failed = true
						failed++
						continue
					}
					catID = &id
				} else if deptID != "" {
					catID = &deptID
				}
				// Tax pairing (ut-docs#512): resolve the row's (dine-in,
				// takeaway) pair onto a tax code — same idempotent,
				// outside-the-item-tx placement as EnsureCategoryUnder
				// above. An explicit equal pair (19,19) and an absent
				// takeaway cell both mean "no override needed", so both
				// normalise to a nil takeaway — they share tax-code space
				// with any plain flat import at the same rate, and only a
				// genuinely different takeaway rate fragments into its own
				// override-carrying code.
				var taxCodeID *string
				// Candidate override for this row — merged into
				// takeawayOverrides only once tx.Commit() below actually
				// lands the item (ut-docs#535), same promote-after-commit
				// pattern as stockWarning/stockRecorded further down: a row
				// whose tax pairing resolves fine but whose item insert then
				// fails must not leave an inert entry for a tax code nothing
				// ends up using.
				var pendingOverrideBP *int
				if it.HasTax {
					var takeawayBP *int
					if it.HasTakeaway && it.TakeawayRateBP != it.TaxRateBP {
						bp := it.TakeawayRateBP
						takeawayBP = &bp
					}
					id, taxCreated, err := repo.FindOrCreateTaxCode(r.Context(), it.TaxRateBP, takeawayBP)
					if err != nil {
						log.Printf("[import] find/create tax code (%d,%v) for %q: %v", it.TaxRateBP, takeawayBP, it.Name, err)
						rows[i].Status = T("import.status.tax_code_failed")
						rows[i].Failed = true
						failed++
						continue
					}
					if taxCreated {
						taxCodesCreated++
					}
					taxCodeID = &id
					pendingOverrideBP = takeawayBP
				}
				// A parseable takeaway rate with no dine-in rate can't
				// resolve a (dine-in, takeaway) pair — the item lands on
				// the till's default rate, same as any other no-tax-column
				// row, but silently so is the exact class of VAT loss this
				// card exists to prevent, just via an odd column
				// combination (review finding N1, ut-docs#512, 2026-08-09).
				// Folded into the warnings slice below, not set here — this
				// runs before BeginTx, before that slice exists.
				takeawayOnlyNoDineIn := taxCodeID == nil && it.HasTakeaway
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
					log.Printf("[import] begin transaction for item %q: %v", it.Name, err)
					rows[i].Status = T("import.status.item_failed")
					rows[i].Failed = true
					failed++
					continue
				}
				itemID, err := repo.CreateItemTx(r.Context(), tx, pos.ItemInput{
					Name: it.Name, SKU: it.SKU, BasePrice: it.PriceMinor,
					Description: it.Description, CategoryID: catID,
					TaxCodeID: taxCodeID,
					Unit:      "each", IsWeighed: it.IsWeighed, IsActive: true,
				})
				if err != nil {
					_ = tx.Rollback()
					log.Printf("[import] create item %q: %v", it.Name, err)
					rows[i].Status = T("import.status.item_failed")
					rows[i].Failed = true
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
					stockWarning = fmt.Sprintf(T("import.status.stock_negative_quantity"), it.Stock)
				case it.HasStock && it.Stock > 0 && locErr != nil:
					// EnsureStockLocation ran once, outside the loop; when it
					// failed, every row with stock would otherwise lose it
					// silently while still reading "created" — the exact bug
					// this card fixes, one branch over.
					log.Printf("[import] ensure stock location: %v", locErr)
					stockWarning = T("import.status.stock_location_failed")
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
						log.Printf("[import] record stock movement for item %s: %v", itemID, err)
						stockWarning = T("import.status.stock_movement_failed")
					} else {
						stockRecorded = true
					}
				}
				if err := tx.Commit(); err != nil {
					log.Printf("[import] commit item %q: %v", it.Name, err)
					rows[i].Status = T("import.status.item_failed")
					rows[i].Failed = true
					failed++
					continue
				}
				// The row's item is now durably committed — only now does
				// its tax-override candidate (if any) get promoted into the
				// set that actually gets merged into ut-plugin-tax-de's
				// setting after the loop (ut-docs#535).
				if pendingOverrideBP != nil {
					takeawayOverrides[*taxCodeID] = *pendingOverrideBP
				}
				// Row-level warnings accumulate rather than short-circuit —
				// a barcode attach failure must not also skip the
				// stock-import outcome below it (ut-docs#293) — both the
				// reason and the stock outcome need to survive into the
				// row's status, so neither branch clobbers the other's
				// message.
				var warnings []string
				if it.Barcode != "" {
					// Pass the symbology catimport already decoded as an
					// explicit BarcodeType so AddBarcode does NOT re-run
					// registry inference on it.Barcode (which is the decoded
					// LookupKey, not the raw code — re-matching it can pick a
					// different, wrong symbology, or fail under a narrowed
					// enabled set) — decode once, in catimport (ut-docs#936
					// review finding F1).
					if err := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
						Barcode: it.Barcode, BarcodeType: it.BarcodeType, ItemID: itemID, IsPrimary: true,
					}); err != nil {
						// Names the conflicting item/variant instead of its
						// raw ID; logs the ID either way (ut-docs#303).
						warnings = append(warnings, common.FriendlyBarcodeConflict(r.Context(), repo, locale, err))
					}
				} else if it.BarcodeIssue != "" {
					// The CSV carried a barcode value, but it matched none
					// of the shop's enabled symbologies (ADR-0059 Decision
					// §3, ut-docs#936) — only reachable once a shop has
					// disabled the default permissive catch-alls
					// (CODE128/INTERNAL_PLU). The item still imports, but
					// silently dropping the barcode with no trace is the
					// same defect class this card exists to fix (ut-docs#293).
					warnings = append(warnings, translateBarcodeIssue(T, it))
				}
				// A present-but-unparseable tax cell imported the row at the
				// till's default rate — compliance-sensitive, so the drop is
				// warned about, never silent (ut-docs#512; same defect class
				// as the dropped-barcode fix, ut-docs#293).
				if it.TaxIssue != "" {
					warnings = append(warnings, translateTaxIssue(T, it.TaxIssue, it.TaxIssueRaw))
				}
				if it.TakeawayTaxIssue != "" {
					warnings = append(warnings, translateTaxIssue(T, it.TakeawayTaxIssue, it.TakeawayTaxIssueRaw))
				}
				if takeawayOnlyNoDineIn {
					warnings = append(warnings, T("import.status.tax_takeaway_only"))
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
					rows[i].Status = T("import.status.created") + "; " + strings.Join(warnings, "; ")
					rows[i].Warned = true
					warned++
				} else {
					rows[i].Status = T("import.status.created")
				}
				created++
			}
			// Populate ut-plugin-tax-de's takeaway_rate_overrides once, after
			// the loop, so the merchant never hand-writes that JSON
			// (ut-docs#512). Best-effort by design: the rows above are real,
			// committed inventory — a plugin-setting hiccup must never fail
			// them — but a failure IS surfaced in the summary line below so
			// someone knows to check the plugin's settings. With the plugin
			// not installed this is silently skipped: core has still done its
			// whole job (correct-rate tax codes), a valid outcome, not an
			// error. Merge-not-clobber: a key already present in the setting
			// (a merchant's hand-set override) is never overwritten.
			if len(takeawayOverrides) > 0 {
				pluginRepo := data.NewPluginRepo(d.Db)
				if active, err := pluginRepo.PluginActive(r.Context(), taxDePluginID); err != nil {
					log.Printf("[import] check %s installed: %v", taxDePluginID, err)
					overridesFailed = true
				} else if active {
					overridesSet, overridesFailed = mergeTakeawayOverrides(r.Context(), pluginRepo, takeawayOverrides)
				} else if _, found, err := pluginRepo.GetPlugin(r.Context(), taxDePluginID, ""); err != nil {
					// Existence check itself failed — same best-effort
					// treatment as the PluginActive error branch above: a
					// plugin-setting hiccup never fails the (already
					// committed) catalog rows, only the summary line.
					log.Printf("[import] check %s existence: %v", taxDePluginID, err)
					overridesFailed = true
				} else if found {
					// Installed but disabled (ut-docs#531): distinguish this
					// from "not installed at all" — a merchant importing
					// before enabling the plugin gets correct tax codes but
					// silently no takeaway overrides, and re-enabling later
					// won't retroactively fix this import's rows.
					overridesPluginDisabled = true
				}
				// found == false (not installed at all): stays silent, per
				// #512's original design — a shop with no German tax plugin
				// genuinely has nothing to configure.
			}
			_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "catalog", "-", "import",
				map[string]any{"format": res.Format, "created": created, "warned": warned, "failed": failed, "rows": len(rows),
					"tax_codes_created": taxCodesCreated, "takeaway_overrides_set": overridesSet,
					"takeaway_overrides_plugin_disabled": overridesPluginDisabled,
					// ut-docs#970: record what currency these rows were
					// actually priced under, alongside whether this request
					// is the one that just confirmed it (vs. an already-
					// confirmed till committing as normal).
					"currency": httpx.ActiveCurrency().Code, "currency_confirmed_this_import": justConfirmedCurrency},
				time.Now().UTC().Format(time.RFC3339), "")
		}

		var b strings.Builder
		if !currencyConfirmed {
			// Preview is never gated (it writes nothing) — but must still
			// tell the operator the currency shown is only the unconfirmed
			// default, not something anyone chose.
			fmt.Fprintf(&b, `<p class="notice-block-warn"><span class="row-warn-icon" aria-hidden="true">⚠</span>%s</p>`,
				fmt.Sprintf(htmlEscape(T("import.warning.currency_unconfirmed")), htmlEscape(httpx.ActiveCurrency().Code)))
		}
		if commit {
			fmt.Fprintf(&b, `<p><strong>✓ %s: %d — %s: %d — %s: %d</strong>`,
				T("import.created"), created, T("import.warned"), warned,
				T("import.skipped"), len(rows)-created)
			if overridesFailed {
				fmt.Fprintf(&b, ` — <span class="row-warn-icon" aria-hidden="true">⚠</span>%s`,
					T("import.status.tax_overrides_not_saved"))
			} else if overridesPluginDisabled {
				fmt.Fprintf(&b, ` — <span class="row-warn-icon" aria-hidden="true">⚠</span>%s`,
					T("import.status.tax_overrides_plugin_disabled"))
			}
			b.WriteString(`</p>`)
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
			// A warned row must be visually distinct from BOTH a clean row
			// and a failed/skipped one — a status pill/icon, not just the
			// row's own text, so it doesn't rely on the operator reading
			// every status cell to notice it (ut-docs#303 review: 3
			// warnings sat unnoticed among 209 rows that all rendered
			// identically). Colour is never the only signal (icon too),
			// and both are CSS vars so it stays legible across themes.
			cls, statusHTML := "", htmlEscape(row.Status)
			switch {
			case row.Warned:
				cls = ` class="row-warn"`
				statusHTML = `<span class="row-warn-icon" aria-hidden="true">⚠</span>` + statusHTML
			case row.Skipped || row.Failed:
				cls = ` class="muted"`
			}
			fmt.Fprintf(&b, `<tr%s><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				cls, htmlEscape(row.Name), httpx.FormatMoney(row.PriceMinor, locale),
				htmlEscape(row.Barcode), htmlEscape(row.Category), statusHTML)
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

// renderImportCurrencyConfirm is ut-docs#970's commit-time gate response: a
// fresh till defaults to GBP, and nothing distinguishes "the operator chose
// GBP" from "nobody has ever looked" — so committing a catalogue import
// while the till's currency has never been explicitly confirmed
// (common.KeyCurrencyConfirmed) must stop and ask, not silently price every
// row under whatever the (possibly wrong) default happens to be. The
// operator either confirms the shown default is correct or picks the
// currency the file's prices are actually in; either way the request round-
// trips back through POST /api/import with confirm_currency set (handled at
// the top of that handler, above), never through a second route.
func renderImportCurrencyConfirm(w http.ResponseWriter, T func(string) string) {
	active := httpx.ActiveCurrency()
	var b strings.Builder
	// ut-docs#970 review (F6): a block-level class that's actually styled —
	// .row-warn on its own only has a `tr.row-warn td` rule, so this block
	// previously rendered with no warning treatment at all.
	b.WriteString(`<div class="notice-block-warn">`)
	fmt.Fprintf(&b, `<p><strong>%s</strong></p><p>%s</p>`,
		htmlEscape(T("import.currency_confirm.title")), htmlEscape(T("import.currency_confirm.help")))
	// ut-docs#970 review (F4): picking a different currency here switches
	// the till's live currency, same as the Settings currency card — same
	// warning that card already carries (no new locale key needed), because
	// the till isn't necessarily empty (the setup wizard's starter catalogue,
	// or a prior confirmed-currency import, may already hold priced items).
	fmt.Fprintf(&b, `<p>%s</p>`, htmlEscape(T("settings.currency.warning")))
	b.WriteString(`<label>` + htmlEscape(T("settings.currency.title")) + ` <select name="confirm_currency" id="import-confirm-currency" form="import-form">`)
	for _, c := range httpx.Currencies() {
		selected := ""
		if c.Code == active.Code {
			selected = " selected"
		}
		fmt.Fprintf(&b, `<option value="%s"%s>%s</option>`, htmlEscape(c.Code), selected, htmlEscape(c.Name))
	}
	b.WriteString(`</select></label> `)
	// hx-encoding is required here, not optional: this button lives outside
	// <form id="import-form"> (it's rendered into the swapped #import-result
	// div), so without an explicit multipart encoding htmx falls back to
	// urlencoded and the included file input serializes as the literal
	// string "[object File]" instead of the actual upload — confirmed by
	// driving this in a real browser while testing (ut-docs#970).
	fmt.Fprintf(&b, `<button class="btn primary" type="button" hx-post="/api/import" hx-target="#import-result" hx-swap="innerHTML" hx-encoding="multipart/form-data" hx-include="#import-form, #import-confirm-currency" hx-vals=%s>%s</button>`,
		`'{"commit":"1"}'`, htmlEscape(T("import.currency_confirm.button")))
	b.WriteString(`</div>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// writeCatalogCSV is G22b's catalog export writer. The CSV round-trips
// with our own importer (column names come from its synonym table), so
// "export → import on a fresh till" is a supported migration path, not an
// accident — which is exactly why the free-typed columns below go through
// csvSafe rather than being left raw: Name/SKU/Barcode/Category/
// Description are reachable by an attacker via an uploaded catalog CSV
// (POST /api/import) and would otherwise detonate as a live formula for
// whoever next opens an export in Excel/LibreOffice (ut-docs#321, same
// defect class as ut-docs#195's invoice/audit fix). Price/stock/weighed/
// active are system-formatted, not free text, so they're left untouched —
// blanket-sanitizing every column corrupted a legitimate negative amount
// last time this was tried (see #195's review). csvSafe's defusing "'" is
// stripped back off by catimport's stripCSVDefuse on re-import, so the
// round trip stays lossless — see that function's doc comment.
func writeCatalogCSV(out io.Writer, rows []data.ExportRow, decimals int) {
	cw := csv.NewWriter(out)
	// "Tax rate"/"Takeaway tax" match catimport's own column synonyms
	// exactly, so the tax pairing round-trips through export → import
	// (ut-docs#512). Blank cells when the item has no tax code / no
	// takeaway rate — system-formatted percent strings, so no csvSafe.
	_ = cw.Write([]string{"Name", "SKU", "Barcode", "Price", "Category",
		"Description", "Sold by weight", "In stock", "Active", "Tax rate", "Takeaway tax"})
	yn := func(b bool) string {
		if b {
			return "Y"
		}
		return "N"
	}
	for _, e := range rows {
		tax, takeaway := "", ""
		if e.HasTax {
			tax = taxrate.FormatPercent(e.TaxRateBP)
			if e.HasTakeaway {
				takeaway = taxrate.FormatPercent(e.TakeawayRateBP)
			}
		}
		_ = cw.Write([]string{
			csvSafe(e.Name), csvSafe(e.SKU), csvSafe(e.Barcode), minorToDecimal(e.PriceMinor, decimals),
			csvSafe(e.Category), csvSafe(e.Description), yn(e.IsWeighed),
			strconv.FormatFloat(e.Stock, 'f', -1, 64), yn(e.IsActive),
			tax, takeaway,
		})
	}
	cw.Flush()
}

// taxDePluginID is the one plugin whose takeaway_rate_overrides setting the
// import can populate — Germany's §12 UStG switch (ut-docs#512). Other
// jurisdictions' plugins have their own setting shapes and are explicitly
// out of this card's scope.
const taxDePluginID = "com.universaltill.tax-de"

// mergeTakeawayOverrides folds the override pairs discovered by one import
// commit into ut-plugin-tax-de's takeaway_rate_overrides setting (a JSON
// object, tax_code_id → takeaway basis points). Add-only: a key already
// present — a merchant's hand-set override — is never overwritten, and an
// existing value that doesn't parse as JSON is left completely untouched
// (clobbering a hand-edit would be worse than skipping). The read-modify-
// write itself runs atomically inside the repo (ut-docs#532) — two imports
// committing close together can no longer race and silently drop one
// other's entry. Returns how many entries were added and whether the step
// failed; failure is the caller's summary-line warning, never a row failure.
func mergeTakeawayOverrides(ctx context.Context, pluginRepo *data.PluginRepo, discovered map[string]int) (added int, failed bool) {
	added, err := pluginRepo.MergeAdditiveJSONMapSetting(ctx, taxDePluginID, "takeaway_rate_overrides", discovered)
	if err != nil {
		log.Printf("[import] merge takeaway_rate_overrides: %v", err)
		return 0, true
	}
	return added, false
}

// zipMagic is the local-file-header signature every non-empty ZIP (a .bkp
// backup included) starts with; zipEmptyMagic is the end-of-central-
// directory signature an entirely empty ZIP starts with instead — sniffed
// so /api/import can auto-detect a .bkp upload alongside the existing CSV
// path with no separate route (ut-docs#511).
var zipMagic = []byte{'P', 'K', 0x03, 0x04}
var zipEmptyMagic = []byte{'P', 'K', 0x05, 0x06}

// sniffZipUpload peeks at the upload's first bytes to decide whether it
// looks like a ZIP (→ ParseBkp) or not (→ Parse, the CSV path), then seeks
// back to the start so the chosen parser reads the whole file from byte 0.
// multipart.File already implements io.Seeker (see catimport.ParseBkp's own
// doc comment), so this never needs to buffer the upload itself.
func sniffZipUpload(file multipart.File) (bool, error) {
	var buf [4]byte
	n, err := io.ReadFull(file, buf[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read upload header: %w", err)
	}
	if _, serr := file.Seek(0, io.SeekStart); serr != nil {
		return false, fmt.Errorf("reset upload after sniff: %w", serr)
	}
	if n < 4 {
		return false, nil
	}
	return bytes.Equal(buf[:], zipMagic) || bytes.Equal(buf[:], zipEmptyMagic), nil
}

// translateImportIssue turns a catimport row-level Issue reason code into
// an operator-facing, translated message — catimport itself has no locale
// to translate into (ut-docs#303), so this is where the reason code plus
// its dynamic detail (e.g. the raw price string) becomes prose.
func translateImportIssue(T func(string) string, it catimport.ImportItem) string {
	switch it.Issue {
	case catimport.IssueMissingName:
		return T("import.status.missing_name")
	case catimport.IssueBadPrice:
		return fmt.Sprintf(T("import.status.bad_price"), it.IssueDetail)
	case catimport.IssueSourceDeleted:
		return T("import.status.source_deleted")
	case catimport.IssueNotSellable:
		return T("import.status.not_sellable")
	case catimport.IssueDuplicateSKUInFile:
		return T("import.status.duplicate_sku_in_file")
	default:
		// A reason code with no case here (catimport grew one this
		// switch doesn't know about yet) must never put machine text on
		// the operator's screen — the exact regression this card exists
		// to prevent (ut-docs#303 review).
		log.Printf("[import] unrecognised Issue reason code %q", it.Issue)
		return T("import.status.unknown_issue")
	}
}

// translateBarcodeIssue is translateImportIssue's counterpart for the
// (non-blocking) BarcodeIssue reason code.
func translateBarcodeIssue(T func(string) string, it catimport.ImportItem) string {
	switch it.BarcodeIssue {
	case catimport.BarcodeIssueNoSymbologyMatch:
		return fmt.Sprintf(T("import.status.barcode_no_symbology_match"), it.BarcodeIssueRaw)
	default:
		log.Printf("[import] unrecognised BarcodeIssue reason code %q", it.BarcodeIssue)
		return T("import.status.unknown_issue")
	}
}

// translateTaxIssue is translateBarcodeIssue's counterpart for the
// (non-blocking) tax-cell reason codes — one function serves both the
// dine-in and takeaway columns, since the message shape is the same.
func translateTaxIssue(T func(string) string, code, raw string) string {
	switch code {
	case catimport.TaxIssueUnparseable:
		return fmt.Sprintf(T("import.status.tax_unparseable"), raw)
	default:
		log.Printf("[import] unrecognised tax issue reason code %q", code)
		return T("import.status.unknown_issue")
	}
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
