package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/taxrate"
)

// asciiFoldLower folds only ASCII 'A'-'Z' to lowercase, matching SQLite's
// `COLLATE NOCASE` exactly (NOCASE folds ASCII only, unlike Go's
// Unicode-aware strings.ToLower). Used to build cache keys that must agree
// with a NOCASE DB lookup on which distinct values are "the same" — see
// ensureCategoryCached below.
func asciiFoldLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

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
		// ut-docs#1168: a setup-wizard preview that couldn't be auto-committed
		// (see commitStagedImportForSetup) lands the now-logged-in operator
		// here instead of a bare /import, so the file they already browsed to
		// and previewed is still one click away rather than a re-upload.
		httpx.Render("ui/pages/import.html", map[string]any{
			"title":     "Import",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"stagedID":  strings.TrimSpace(r.URL.Query().Get("staged_id")),
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
	//
	// Android does NOT use this endpoint (ut-docs#1258): os.UserHomeDir()
	// resolves to the app's private sandboxed data directory there (if it
	// resolves at all), which is invisible to the user in any file manager —
	// a raw os.Create has nowhere meaningful to write on that OS. catalog.html
	// branches on window.AndroidKiosk (the same presence-check idiom
	// settings.html's exitLockdown() call uses) and instead navigates to the
	// GET /api/catalog/export download above, which the native shell's
	// WebView.DownloadListener (MainActivity.kt) hands to Android's own
	// DownloadManager — the OS-supported way into the shared Downloads
	// collection. This handler stays desktop/Pi-only.
	//
	// Every failure branch below used to collapse into the one generic
	// "import.export_save_failed" notice with no server-side detail at all —
	// undiagnosable from logs alone. Each now logs which step failed before
	// showing the operator the same generic message (ut-docs#1258 AC).
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
			logging.L().Errorf("catalog export-save: os.UserHomeDir: %v", herr)
			httpx.RenderNotice(w, locale, "error", "import.export_save_failed")
			return
		}
		dstDir := filepath.Join(home, "Downloads")
		if mkErr := os.MkdirAll(dstDir, 0o755); mkErr != nil {
			logging.L().Errorf("catalog export-save: os.MkdirAll(%s): %v", dstDir, mkErr)
			httpx.RenderNotice(w, locale, "error", "import.export_save_failed")
			return
		}
		dst := filepath.Join(dstDir, "catalog-"+time.Now().UTC().Format("2006-01-02")+".csv")
		f, ferr := os.Create(dst)
		if ferr != nil {
			logging.L().Errorf("catalog export-save: os.Create(%s): %v", dst, ferr)
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
		usedFirstBootExemption := false
		if !canPerform(d, r, "import_export") {
			// ut-docs#1168: the setup wizard's restore step lets a migrating
			// shop browse to their export/backup file and preview it inline,
			// before any admin account exists — same auth-exempt,
			// NeedsFirstBoot-gated tier as POST /api/setup/join and friends
			// (setup_page.go). NeedsFirstBoot flips false the instant the
			// wizard's own PIN step creates the admin account, so this
			// window is exactly as narrow as every other first-boot
			// exemption, and commitStagedImportForSetup (below) is the only
			// caller that ever actually commits through this exemption —
			// the wizard's own upload panel only ever previews (commit=0).
			//
			// Deliberately narrower than a bare NeedsFirstBoot check: that
			// method only asks "does any PIN-bearing user exist in the DB
			// at all", which says nothing about THIS request. An already-
			// authenticated-but-insufficiently-privileged request (a
			// cashier session denied import_export above) must still be
			// denied even in the rare state where no user has a PIN set
			// yet — auth.FromContext resolving a session at all is proof
			// this request isn't the anonymous, pre-admin wizard case this
			// exemption exists for. A nil AuthSvc (some minimal test/embed
			// setups never wire one) fails closed the same way canPerform
			// already does elsewhere in this file.
			if _, hasSession := auth.FromContext(r.Context()); hasSession || d.AuthSvc == nil {
				common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
				return
			}
			firstBoot, ferr := d.AuthSvc.NeedsFirstBoot(r.Context())
			if ferr != nil || !firstBoot {
				common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
				return
			}
			usedFirstBootExemption = true
		}
		if err := r.ParseMultipartForm(20 << 20); err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
			return
		}
		commit := r.FormValue("commit") == "1"
		// ut-docs#1168: the first-boot exemption above covers PREVIEW ONLY.
		// The wizard's own upload panel never sends commit=1 — its
		// preview's staged_id instead rides the wizard's final submit and
		// is replayed as a real commit by commitStagedImportForSetup
		// (import_stage.go), by which point the request carries the
		// just-created admin's identity via auth.WithUser and never needs
		// this exemption at all. Checked here, after the parse, rather
		// than by peeking at commit's value earlier: an early r.FormValue
		// call would parse under Go's default 32MB memory cap instead of
		// this handler's explicit 20MB one below, and ParseMultipartForm
		// no-ops on an already-parsed request — silently widening the
		// size cap for every caller, not just this exemption.
		if usedFirstBootExemption && commit {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// ut-docs#1168: suppress the interactive problem-grid/barcode-
		// opt-in controls and the repeated bottom Import button (below) on
		// a preview served to the wizard's own upload panel (setup.html
		// sends wizard=1) — none of them are safe to act on there.
		// Country/currency aren't saved until the wizard's own final
		// submit, so a same-session commit from this preview (the bottom
		// button's whole purpose) could label prices under a stale
		// currency; the row-level corrections those controls capture
		// (row_include_*, use_item_numbers_as_barcodes) are never
		// forwarded by commitStagedImportForSetup for the same reason — it
		// only ever replays a bare commit, once real state is saved. The
		// operator still gets the full interactive pipeline, unsuppressed,
		// on /import — either via the auto-commit's own fallback
		// (staged_id) or by finishing setup and importing again for real.
		wizardPreview := r.FormValue("wizard") == "1"
		stagedID := strings.TrimSpace(r.FormValue("staged_id"))

		// Resolved up front (ut-docs#303): every row status below is a
		// locale key, not English prose, so T needs to be live before the
		// preview loop builds the first one, and before the Parse error
		// below (the first thing an operator sees on a wrong-format file).
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)

		// Which bytes does this request act on? (ut-docs#601)
		//  - Commit carrying a staged_id: the byte-identical copy staged at
		//    preview time — never a fresh upload. The per-row override
		//    fields refer to row indexes that are only stable against the
		//    exact bytes the operator actually previewed.
		//  - Everything else (preview, or a never-previewed direct commit):
		//    the uploaded file, exactly as before this card.
		var file multipart.File
		var fileSize int64
		usingStaged := false
		// preserveStaged: set while a staged commit is inside the currency-
		// confirm gate (ut-docs#970, below) — EVERY early return in that
		// section (the confirm prompt, a rejected currency code, a re-parse
		// or settings-write failure) must hand the staged copy BACK to the
		// registry instead of consuming it, so a subsequent legitimate
		// resubmit re-reads the same copy and the operator's problem-grid
		// overrides still apply (ut-docs#601 review, F5: originally only the
		// prompt's own return preserved it). Every other way a staged commit
		// finishes — passing the gate and committing, or failing before the
		// gate — consumes the copy; an abandoned preserved copy is reclaimed
		// by the registry's TTL prune.
		preserveStaged := false
		if commit && stagedID != "" {
			path, ok := takeStagedCatalogUpload(stagedID)
			if !ok {
				// Expired/unknown: refuse rather than silently falling back
				// to the re-sent upload — that would commit WITHOUT the
				// operator's corrections while looking like it applied them.
				http.Error(w, T("import.error.stage_expired"), http.StatusBadRequest)
				return
			}
			f, oerr := os.Open(path)
			if oerr != nil {
				log.Printf("[import] open staged upload: %v", oerr)
				_ = os.Remove(path)
				http.Error(w, T("import.error.stage_expired"), http.StatusInternalServerError)
				return
			}
			// This commit consumes the staged copy — success or failure,
			// it is removed once the request finishes (design point 4) —
			// UNLESS it detoured to the currency-confirm prompt, in which
			// case the copy goes back to the registry for the confirmed
			// resubmit (preserveStaged above).
			defer func() {
				_ = f.Close()
				if preserveStaged {
					restageCatalogUpload(stagedID, path)
				} else {
					_ = os.Remove(path)
				}
			}()
			fi, serr := f.Stat()
			if serr != nil {
				log.Printf("[import] stat staged upload: %v", serr)
				http.Error(w, T("import.error.invalid_file"), http.StatusInternalServerError)
				return
			}
			file, fileSize, usingStaged = f, fi.Size(), true
		} else {
			f, header, ferr := r.FormFile("file")
			if ferr != nil {
				http.Error(w, "csv file required", http.StatusBadRequest)
				return
			}
			defer f.Close()
			file, fileSize = f, header.Size
		}
		var err error

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

		// Same shared registry matcher AddBarcode/the scan path use (ADR-0059
		// Decision §3, ut-docs#936) — never brick an upload on a settings
		// read failure, same rule as AddBarcode: EnabledBarcodeSymbologies
		// already returns the compatibility-preserving default set alongside
		// any error. Fetched once, above the format branch, so both parsers
		// and every re-parse below (currency switch, barcode opt-in) share
		// the same read.
		enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
		if symErr != nil {
			log.Printf("[import] enabled symbologies unavailable, using defaults: %v", symErr)
		}
		// ut-docs#1224: the operator's per-import opt-in to derive a barcode
		// from each item's own number, offered as an inline checkbox on the
		// preview (see barcodelessCatalog/the preview render below) when the
		// source carries no barcodes of its own. An unchecked checkbox sends
		// no field at all, so "never previewed," "previewed but not shown
		// the checkbox," and "shown but left unticked" all read the same
		// way here: false, same as the field simply not existing before
		// this card — never a gate, never a second round-trip.
		useItemNumbersAsBarcodes := r.FormValue("use_item_numbers_as_barcodes") == "1"
		// ut-docs#1356: on the FIRST render of this checkbox for THIS import
		// — never a re-preview, never a commit — pre-tick it from the shop's
		// own default (CatalogImportBarcodeFromSKUDefaultKey, settings page)
		// instead of always starting unticked. "First render" is judged the
		// same way the rest of this handler already distinguishes a fresh
		// upload from a round-trip: no incoming staged_id at all (a re-
		// preview/currency-confirm resubmit always carries the PREVIOUS
		// preview's hidden staged_id field, forged by stagedFormID below —
		// see its own comment) and not a commit (a direct, never-previewed
		// commit must behave exactly as before this card, matching
		// TestImport_BarcodelessCatalog_DirectCommit_NoGate). Suppressed on
		// a wizard preview too: that render never shows the checkbox at all
		// (the gate below is itself !wizardPreview-gated), and its eventual
		// real commit (commitStagedImportForSetup) never forwards this field
		// regardless — this only avoids the wizard's OWN preview table
		// silently showing derived barcodes with no checkbox to explain why.
		// This can only ever ADD a tick to what the raw form value already
		// computed above — it never clears one an explicit submission set,
		// and it plays no part at all in a commit that already has its own
		// real field to read (a ticked-and-resubmitted preview, or a bare
		// commit, both read their own r.FormValue untouched by this block).
		if !useItemNumbersAsBarcodes && !commit && !wizardPreview && stagedID == "" {
			if def, found, derr := settingsRepo.Get(r.Context(), data.CatalogImportBarcodeFromSKUDefaultKey); derr == nil && found && def == "1" {
				useItemNumbersAsBarcodes = true
			}
		}

		var res catimport.Result
		if isBkp {
			res, err = catimport.ParseBkp(file, fileSize, httpx.ActiveCurrency().Decimals, enabledIDs, useItemNumbersAsBarcodes)
		} else {
			res, err = catimport.Parse(file, httpx.ActiveCurrency().Decimals, enabledIDs, useItemNumbersAsBarcodes)
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
			// EVERY early return inside this gated section — the confirm
			// prompt below, an unknown confirm_currency code, a seek/re-parse
			// failure, a settings write failure — leaves the operator with a
			// legitimate resubmit still ahead of them, so on the staged path
			// ALL of them must hand the staged copy back rather than destroy
			// it with the operator's corrections inside (ut-docs#601 review
			// F5: originally only the first return preserved it). Cleared
			// again only when the gate is actually passed, at the end of this
			// block: from there the commit proceeds and consumes the copy. An
			// abandoned copy is reclaimed by the registry's TTL prune.
			if usingStaged {
				preserveStaged = true
			}
			confirmCode := strings.ToUpper(strings.TrimSpace(r.FormValue("confirm_currency")))
			if confirmCode == "" {
				// First attempt to commit with an unconfirmed currency:
				// write nothing, ask instead. The prompt must re-emit the
				// staged_id and the operator's submitted override fields —
				// the originals lived inside the #import-result div this
				// prompt is about to replace, so without re-emission the
				// confirmed resubmit would silently commit WITHOUT the
				// operator's corrections (ut-docs#601 review).
				renderImportCurrencyConfirm(w, T, stagedID, r.MultipartForm.Value)
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
					res, err = catimport.ParseBkp(file, fileSize, chosen.Decimals, enabledIDs, useItemNumbersAsBarcodes)
				} else {
					res, err = catimport.Parse(file, chosen.Decimals, enabledIDs, useItemNumbersAsBarcodes)
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
			// Gate passed: this commit now proceeds through the override
			// application and write loop, which consume the staged copy.
			preserveStaged = false
		}

		// ut-docs#601: apply the operator's per-row overrides from the
		// preview's problem grid. ONLY on the staged-commit path — a
		// never-previewed commit must behave exactly as before this card —
		// and ONLY for the allow-listed forceable issue types
		// (forceableImportIssue): the integrity-sensitive skips (duplicate
		// in file, barcode/SKU already in catalog, …) can never be forced
		// through, no matter what the client submits — that's how silent
		// duplicate catalog entries happen. Runs AFTER the currency-confirm
		// block above so a corrected price parses under the decimals the
		// rows will actually commit under. overrideNotes carries the
		// translated reason a ticked row still stayed skipped (blank or
		// unparseable correction) for the row's rendered status.
		overrideNotes := map[int]string{}
		if commit && usingStaged {
			decimals := httpx.ActiveCurrency().Decimals
			// In-file duplicate veto (ut-docs#601 review F1): a forceable
			// issue (missing_name/bad_price) can mask the fact that the row's
			// SKU/PLU also collides with ANOTHER row in this same parse — the
			// .bkp parser's own seen-set only flags a duplicate whose clean
			// twin came EARLIER in the file, and a flagged row never registers
			// its PLU (deliberately, see bkp.go). Without this check, a
			// corrected-name override made such a row importable: it raced the
			// legitimate row to the items.sku UNIQUE constraint, and whichever
			// lost surfaced as a baffling generic item_failed. Same guarantee
			// as the DB-level SKUExists/BarcodeExists checks below, which
			// every un-skipped row (forced or not) still goes through: an
			// in-file duplicate can never be forced through, no matter what
			// the client submits. Seeded with every cleanly-importable row's
			// SKU; each ACCEPTED override then claims its own SKU too, so of
			// two forced rows sharing a SKU only the first can land.
			inFileSKU := map[string]bool{}
			for i := range res.Items {
				if res.Items[i].Issue == "" && res.Items[i].SKU != "" {
					inFileSKU[res.Items[i].SKU] = true
				}
			}
			for i := range res.Items {
				if r.FormValue(fmt.Sprintf("row_include_%d", i)) != "1" {
					continue
				}
				field, forceable := forceableImportIssue(res.Items[i].Issue)
				if !forceable {
					continue
				}
				if sku := res.Items[i].SKU; sku != "" && inFileSKU[sku] {
					// Checked before the correction itself: no corrected
					// name/price could ever make this row importable, so the
					// status says the real, terminal reason.
					overrideNotes[i] = T("import.status.duplicate_sku_in_file")
					continue
				}
				switch field {
				case "name":
					name := strings.TrimSpace(r.FormValue(fmt.Sprintf("row_name_%d", i)))
					if name == "" {
						overrideNotes[i] = T("import.problem_grid.name_required")
						continue
					}
					res.Items[i].Name = name
					res.Items[i].Issue, res.Items[i].IssueDetail = "", ""
				case "price":
					rawPrice := strings.TrimSpace(r.FormValue(fmt.Sprintf("row_price_%d", i)))
					if rawPrice == "" {
						overrideNotes[i] = T("import.problem_grid.price_required")
						continue
					}
					// Same parser the file's own price cells go through —
					// one price grammar on this page, not two.
					minor, perr := catimport.ParsePrice(rawPrice, decimals)
					if perr != nil {
						overrideNotes[i] = fmt.Sprintf(T("import.problem_grid.price_invalid"), rawPrice)
						continue
					}
					res.Items[i].PriceMinor = minor
					res.Items[i].Issue, res.Items[i].IssueDetail = "", ""
				}
				// Override accepted (Issue cleared): the row now claims its
				// SKU, so a second forced row sharing it is vetoed above.
				if res.Items[i].Issue == "" && res.Items[i].SKU != "" {
					inFileSKU[res.Items[i].SKU] = true
				}
			}
		}

		// Annotate duplicates (server truth) for both preview and commit.
		type rowView struct {
			catimport.ImportItem
			Status   string // translated display text
			Skipped  bool   // preview-time issue/duplicate — never entered the commit loop as importable
			Warned   bool   // created, but with a warning
			Failed   bool   // commit-time failure (category/department/item creation)
			Idx      int    // stable 0-based row index for this parse (ut-docs#601) — field names row_include_<Idx> etc.
			FixField string // "name"/"price" when the row's issue is forceable with an inline correction, else ""
		}
		var rows []rowView
		importable := 0
		for i, it := range res.Items {
			status := T("import.status.ok")
			skipped := false
			fixField := ""
			switch {
			case overrideNotes[i] != "":
				// Ticked to import but the correction didn't validate —
				// stays skipped, and the status says why.
				status, skipped = overrideNotes[i], true
			case it.Issue != "":
				status, skipped = translateImportIssue(T, it), true
				fixField, _ = forceableImportIssue(it.Issue)
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
			rows = append(rows, rowView{ImportItem: it, Status: status, Skipped: skipped, Idx: i, FixField: fixField})
		}

		// ut-docs#601: a preview stages the upload so the follow-up commit
		// can re-read the byte-identical file and honour per-row overrides.
		// A staging failure degrades gracefully: the preview still renders,
		// just without the interactive problem grid (today's behavior).
		stagedFormID := ""
		if !commit {
			if stagedID != "" {
				// Re-preview: the previous staged copy's hidden input was
				// still in the form when the operator clicked Preview again
				// — supersede it instead of letting it pile up until TTL.
				discardStagedCatalogUpload(stagedID)
			}
			if sid, serr := stageCatalogUpload(file); serr != nil {
				log.Printf("[import] stage preview upload: %v", serr)
			} else {
				stagedFormID = sid
			}
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
			// Local per-run caches (ut-docs#1322, perf audit
			// 2026-08-30-performance-audit.md section F finding #3):
			// EnsureCategoryUnder/FindOrCreateTaxCode are idempotent and
			// parent/pair-scoped, so a distinct (name, parent) or (rate,
			// takeaway) value resolves to the same id for every row that
			// shares it. A 2,000-3,000 row import across ~30 categories
			// was issuing thousands of redundant lookup queries instead of
			// ~30-60 — cache on first miss per distinct value instead of
			// re-querying per row. Scoped to this one commit run, not
			// package-level: rows never span two HTTP requests.
			categoryCache := map[string]string{}
			ensureCategoryCached := func(name, parentID string) (string, error) {
				// Fold ONLY ASCII A-Z, matching EnsureCategoryUnder's own
				// `COLLATE NOCASE` lookup exactly (SQLite's NOCASE folds
				// ASCII only). strings.ToLower is Unicode-aware and would
				// fold e.g. "GETRÄNKE"/"Getränke" onto the same cache key
				// even though NOCASE treats Ä and ä as distinct — that
				// mismatch would divert a row into the wrong existing
				// category the DB itself would never have merged
				// (independent review finding B1, ut-docs#1322).
				key := asciiFoldLower(strings.TrimSpace(name)) + "\x00" + parentID
				if id, ok := categoryCache[key]; ok {
					return id, nil
				}
				id, err := repo.EnsureCategoryUnder(r.Context(), name, parentID)
				if err != nil {
					return "", err
				}
				categoryCache[key] = id
				return id, nil
			}
			taxCodeCache := map[string]string{}
			findOrCreateTaxCodeCached := func(rateBP int, takeawayBP *int) (string, bool, error) {
				key := strconv.Itoa(rateBP) + "\x00"
				if takeawayBP != nil {
					key += strconv.Itoa(*takeawayBP)
				}
				if id, ok := taxCodeCache[key]; ok {
					// Already resolved earlier in this run — never
					// re-report "created" for a cache hit, or a repeated
					// value would inflate taxCodesCreated past the number
					// of rows that actually issued an INSERT.
					return id, false, nil
				}
				id, created, err := repo.FindOrCreateTaxCode(r.Context(), rateBP, takeawayBP)
				if err != nil {
					return "", false, err
				}
				taxCodeCache[key] = id
				return id, created, nil
			}
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
					id, err := ensureCategoryCached(it.Department, "")
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
					id, err := ensureCategoryCached(it.Category, deptID)
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
					id, taxCreated, err := findOrCreateTaxCodeCached(it.TaxRateBP, takeawayBP)
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
				// Product photo (ut-docs#1223): a real image ParseBkp
				// resolved from the .bkp archive's documents.zip is
				// decoded/re-encoded and written exactly the way a manual
				// upload does (POST /api/catalog/item/image), then recorded
				// with the unconditional-overwrite SetItemThumbnail — this
				// genuinely is "a real photo" (see that method's own doc
				// comment for why that's the right one, not
				// EnsureDefaultThumbnail). Falls back to the ut-docs#1189
				// placeholder icon whenever there's no image, or it fails to
				// decode — same best-effort, never-fail-the-row spirit as
				// that existing path.
				imageSet := false
				if len(it.ImageData) > 0 {
					if img, derr := imaging.Decode(it.ImageData); derr != nil {
						log.Printf("[import] decode image for item %q: %v", it.Name, derr)
						warnings = append(warnings, T("import.status.image_undecodable"))
					} else {
						dir := paths.Data("public", "assets", "items", itemID)
						thumbPath := filepath.Join(dir, "thumb.png")
						writeErr := func() error {
							if err := os.MkdirAll(dir, 0o755); err != nil {
								return fmt.Errorf("create image dir: %w", err)
							}
							out, err := os.Create(thumbPath)
							if err != nil {
								return fmt.Errorf("create thumbnail file: %w", err)
							}
							encErr := png.Encode(out, img)
							closeErr := out.Close()
							if encErr != nil || closeErr != nil {
								// A partial/corrupt file must never linger
								// (independent review, ut-docs#1223):
								// self_order_shop.go's ImageURL resolves
								// thumb.png by path CONVENTION, not via
								// item_images, so a truncated file here
								// would render broken on the self-order
								// kiosk even though item_images correctly
								// still points at the placeholder set
								// below. Best-effort — the row's own
								// outcome never depends on this succeeding.
								if rmErr := os.Remove(thumbPath); rmErr != nil && !os.IsNotExist(rmErr) {
									log.Printf("[import] remove partial thumbnail for item %q: %v", it.Name, rmErr)
								}
								if encErr != nil {
									return fmt.Errorf("encode thumbnail: %w", encErr)
								}
								return fmt.Errorf("close thumbnail file: %w", closeErr)
							}
							return repo.SetItemThumbnail(r.Context(), itemID, "/public/assets/items/"+itemID+"/thumb.png")
						}()
						if writeErr != nil {
							// A resolvable photo that then failed to save
							// must not fail silently (independent review,
							// ut-docs#1223) — same "never silently drop a
							// reference" reasoning as ImageIssue below,
							// just for a failure that happens after
							// resolution rather than during it.
							log.Printf("[import] save real photo for item %q: %v", it.Name, writeErr)
							warnings = append(warnings, T("import.status.image_save_failed"))
						} else {
							imageSet = true
						}
					}
				}
				if !imageSet {
					if err := repo.EnsureDefaultThumbnail(r.Context(), itemID, catimport.PlaceholderIconPath(it.Name, it.Category)); err != nil {
						log.Printf("[import] set placeholder thumbnail for item %q: %v", it.Name, err)
					}
				}
				// The source referenced an image that never turned into
				// usable bytes (dangling path, no documents.zip, oversized
				// entry) — non-blocking, the row still imports with the
				// placeholder icon set just above, but silently dropping
				// the reference would be the same defect class as the
				// dropped-barcode fix (ut-docs#293).
				if it.ImageIssue != "" {
					warnings = append(warnings, translateImageIssue(T, it.ImageIssue, it.ImageIssueRaw))
				}
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
				// A reused source product number was de-duplicated with a
				// suffix, not dropped (ut-docs#1222) — surfaced the same
				// non-blocking way as a dropped barcode/tax rate above, so
				// the operator sees the number was reused rather than
				// silently getting a "-2" SKU with no explanation.
				if it.SKUIssue != "" {
					warnings = append(warnings, translateSKUIssue(T, it.SKUIssue, it.SKUIssueRaw))
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
					overridesSet, overridesFailed = mergeTakeawayOverrides(r.Context(), d.Db, takeawayOverrides)
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
			// ut-docs#1171: the product owner, importing a real 217-item .bkp
			// on the Pi till, couldn't tell the commit had actually happened
			// — the result looked like just another preview. A distinct
			// block (vs. the preview's plain <p>/amber warning) plus a big
			// "Imported N ✓" headline and a View catalog button make the
			// post-commit state unmistakable at a glance.
			// ut-docs#1171 review: an unconditional green .notice-block-
			// success would read as unambiguous success even when rows hit
			// a real, non-duplicate failure (item/category/tax-code
			// creation errors, tallied in `failed`, as opposed to an
			// expected "already in catalog" skip) — false confidence in
			// exactly the state this card exists to make trustworthy. Stay
			// amber (the same treatment a partial preview issue already
			// gets) whenever any row actually failed.
			successClass := "notice-block-success"
			if failed > 0 {
				successClass = "notice-block-warn"
			}
			fmt.Fprintf(&b, `<div class="%s">`, successClass)
			fmt.Fprintf(&b, `<p><strong>%s</strong></p>`, fmt.Sprintf(htmlEscape(T("import.commit_success")), created))
			fmt.Fprintf(&b, `<p>%s: %d — %s: %d — %s: %d`,
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
			fmt.Fprintf(&b, `<a class="btn primary" href="/catalog">%s</a>`, htmlEscape(T("import.view_catalog")))
			b.WriteString(`</div>`)
		} else {
			fmt.Fprintf(&b, `<p><strong>%s: %s · %d %s, %d %s</strong></p>`,
				T("import.detected"), res.Format, importable, T("import.ready"), len(rows)-importable, T("import.with_issues"))
			if stagedFormID != "" {
				// form="import-form": this input lives in the swapped
				// #import-result div, outside <form id="import-form">, so it
				// must be form-associated explicitly to ride along on the
				// Import submit (same reason the currency-confirm select
				// carries the attribute).
				fmt.Fprintf(&b, `<input type="hidden" name="staged_id" value="%s" form="import-form">`, stagedFormID)
			}
			// ut-docs#1224: offered only when the source carries no barcodes
			// of its own (barcodelessCatalog) — an inline, unchecked-by-
			// default checkbox rather than a separate blocking prompt/round-
			// trip: the "Confirm & Import" submit below is a real HTML form
			// submit (type="submit" form="import-form"), so a checkbox left
			// unticked simply never sends use_item_numbers_as_barcodes at
			// all, reading identically to "never asked" — genuinely
			// per-import, genuinely default off, with no extra request.
			//
			// Two review-found gaps (2026-08-30), one fix each condition
			// below closes:
			//  - Sticky across a re-preview (`|| useItemNumbersAsBarcodes`,
			//    `checked` when it's on): barcodelessCatalog(res) is judged
			//    against THIS parse's result, and a re-preview with the box
			//    already ticked parses WITH derived barcodes filled in — so
			//    without this, the box that produced the very rows the
			//    operator is looking at would silently vanish, and the next
			//    Import would commit with none of it. Confirmed empirically
			//    in review: a barcode-less file, ticked, previewed a SECOND
			//    time, showed the derived codes in the table with no
			//    checkbox left to submit them.
			//  - Gated on stagedFormID != "" (matches the interactive
			//    problem-grid controls just above, ut-docs#601's own
			//    `interactive` gate): stageCatalogUpload can fail
			//    (disk/TMPDIR — a documented graceful-degradation path), in
			//    which case renderImportCurrencyConfirm's re-emission
			//    (confirmCarriedOverrideField) never runs at all — it's
			//    scoped to `stagedID != ""` — so a checkbox rendered anyway
			//    would tick, then silently lose its value on a currency-
			//    gate detour with nothing to carry it forward. Never
			//    offering the choice in that rare failure is the safe
			//    degradation; the import itself still proceeds, barcode-
			//    less, exactly as before this card.
			if !wizardPreview && stagedFormID != "" && (barcodelessCatalog(res) || useItemNumbersAsBarcodes) {
				checkedAttr := ""
				if useItemNumbersAsBarcodes {
					checkedAttr = " checked"
				}
				fmt.Fprintf(&b, `<p><label><input type="checkbox" name="use_item_numbers_as_barcodes" value="1" form="import-form"%s> %s</label></p>`,
					checkedAttr, htmlEscape(T("import.barcode_opt_in.label")))
			}
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
		// Problem-row controls render only on an interactive preview — a
		// preview whose upload actually staged (ut-docs#601). Never on a
		// commit response: the grid's whole point is deciding what the NEXT
		// commit does.
		interactive := !commit && stagedFormID != "" && !wizardPreview
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
			if interactive && row.Skipped && row.FixField != "" {
				// Include/skip checkbox plus inline correction input — ONLY
				// for the forceable issue types (missing_name/bad_price,
				// forceableImportIssue). Any other skipped row keeps its
				// passive status text with no controls at all: the server
				// would ignore a ticked include on it anyway, and an inert
				// checkbox with no feedback misleads the operator into
				// thinking something can be done (ut-docs#601 review F3).
				// Required-if-ticked is wired up by the page's own script via
				// data-fix-target. All controls are form-associated
				// (form="import-form") — they live outside the <form>, in the
				// swapped #import-result div. Logical properties only
				// (margin-block-*): fa/ar render RTL.
				statusHTML += fmt.Sprintf(
					`<label class="import-fix-include" style="display:block;margin-block-start:.3rem"><input type="checkbox" name="row_include_%d" value="1" form="import-form" data-fix-target="row-fix-%d"> %s</label>`,
					row.Idx, row.Idx, htmlEscape(T("import.problem_grid.include_label")))
				switch row.FixField {
				case "name":
					statusHTML += fmt.Sprintf(
						`<input type="text" id="row-fix-%d" name="row_name_%d" form="import-form" placeholder="%s" aria-label="%s" style="display:block;margin-block-start:.3rem;max-width:14rem">`,
						row.Idx, row.Idx, htmlEscape(T("import.problem_grid.corrected_name")), htmlEscape(T("import.problem_grid.corrected_name")))
				case "price":
					statusHTML += fmt.Sprintf(
						`<input type="text" id="row-fix-%d" name="row_price_%d" form="import-form" inputmode="decimal" placeholder="%s" aria-label="%s" style="display:block;margin-block-start:.3rem;max-width:8rem">`,
						row.Idx, row.Idx, htmlEscape(T("import.problem_grid.corrected_price")), htmlEscape(T("import.problem_grid.corrected_price")))
				}
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
		if !commit && !wizardPreview {
			// ut-docs#1171: a long preview (the product owner's real
			// 217-item .bkp ran to 209+ rows plus this "… N more"
			// truncation) otherwise strands the operator at the bottom
			// with the only Import control back up at the top of the
			// page. Repeat it here, touch-target sized. type="submit"
			// form="import-form": this button lives outside <form
			// id="import-form"> (rendered into the swapped #import-result
			// div), but form-associating it makes clicking it a genuine
			// submit of that form — same htmx hx-post/multipart encoding
			// the top Import button already triggers, no hx-include/
			// hx-encoding duplication needed.
			fmt.Fprintf(&b, `<div style="margin-block-start:.8rem"><button class="btn primary" type="submit" form="import-form" onclick="document.getElementById('import-commit').value='1'">%s</button></div>`,
				htmlEscape(T("import.import")))
		}
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
//
// When the gated commit was a STAGED one (ut-docs#601), stagedID is the
// preserved staged copy's id and form is the request's parsed multipart
// fields: this prompt fully replaces the #import-result div that held the
// original staged_id hidden input and every problem-grid override control,
// so both must be re-emitted here as hidden form-associated inputs (same
// form="import-form" pattern the preview render uses) or the "Confirm &
// Import" resubmit falls back to the non-staged path and silently discards
// the operator's corrections. Only the allow-listed row_* field names are
// ever reflected back (confirmCarriedOverrideField) — nothing else from the
// request reaches the response HTML. stagedID == "" (a plain never-previewed
// commit) emits none of this, keeping the pre-#601 flow byte-identical.
func renderImportCurrencyConfirm(w http.ResponseWriter, T func(string) string, stagedID string, form map[string][]string) {
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
	if stagedID != "" {
		// form="import-form": these live in the swapped #import-result div,
		// outside <form id="import-form">, so they must be form-associated
		// explicitly to ride along on the confirm resubmit — the exact
		// pattern the preview's own staged_id hidden input uses.
		fmt.Fprintf(&b, `<input type="hidden" name="staged_id" value="%s" form="import-form">`, htmlEscape(stagedID))
		for name, vals := range form {
			if !confirmCarriedOverrideField.MatchString(name) {
				continue
			}
			for _, v := range vals {
				fmt.Fprintf(&b, `<input type="hidden" name="%s" value="%s" form="import-form">`, htmlEscape(name), htmlEscape(v))
			}
		}
	}
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

// barcodelessCatalog reports whether res is worth offering the
// use_item_numbers_as_barcodes checkbox for (ut-docs#1224): at least one row
// that would actually import (Issue == "") carries an item number but no
// barcode of its own. Works uniformly for both formats with no per-format
// branching — the .bkp path never populates Barcode at all (see bkp.go),
// and a CSV with no barcode column or only empty cells leaves it empty too.
// A catalog that already carries real barcodes never matches this, so the
// checkbox never adds friction to the common case.
func barcodelessCatalog(res catimport.Result) bool {
	for _, it := range res.Items {
		if it.Issue == "" && it.SKU != "" && it.Barcode == "" {
			return true
		}
	}
	return false
}

// confirmCarriedOverrideField matches exactly the per-row problem-grid
// field names the commit handler reads (row_include_<i>, row_name_<i>,
// row_price_<i>) plus the barcode opt-in checkbox (use_item_numbers_as_barcodes,
// ut-docs#1224) — the only request fields renderImportCurrencyConfirm ever
// reflects back into the prompt's HTML. An allow-list on purpose, same
// stance as forceableImportIssue: any other submitted field name never
// round-trips through the response.
//
// The checkbox needs this for exactly the same reason row_* does (ut-docs#601):
// a staged commit that also needs the currency-confirm gate has that gate's
// response fully REPLACE #import-result — including the checkbox itself, an
// input outside <form id="import-form"> that only exists there because the
// preview render put it there — so without re-emitting it as a hidden input
// here, an operator who ticked it, then hit the (unrelated) currency gate,
// silently loses their choice on the "Confirm & Import" resubmit: confirmed
// live, driving this exact sequence in a real browser (ut-docs#1224 tester
// note) — the item imported with no barcode despite the box being ticked,
// before this fix.
var confirmCarriedOverrideField = regexp.MustCompile(`^(row_(include|name|price)_[0-9]+|use_item_numbers_as_barcodes)$`)

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
func mergeTakeawayOverrides(ctx context.Context, db *sql.DB, discovered map[string]int) (added int, failed bool) {
	added, err := data.NewPluginRepo(db).MergeAdditiveJSONMapSetting(ctx, taxDePluginID, "takeaway_rate_overrides", discovered)
	if err != nil {
		log.Printf("[import] merge takeaway_rate_overrides: %v", err)
		return 0, true
	}
	if added > 0 {
		// ut-docs#1351: a plugin-settings write changes what the tax plugin
		// answers for a payload that hasn't changed, and pluginTaxRateAsker
		// (tax_hook.go) memoizes answers per bus generation. Every other
		// settings writer bumps — the settings editor
		// (plugin_settings_page.go), the sync/directive rederive path
		// (init.go), permission grant/revoke (plugin_api.go) — but this one
		// didn't, so a "no opinion" cached from a takeaway sale rung BEFORE
		// the import configured the override kept being served (19% instead
		// of the merged 7%) until an unrelated reload happened to bump. The
		// exact pilot shape: import with the plugin disabled seeds tax codes
		// but skips overrides (ut-docs#531 branch); a later re-import merges
		// them into the SAME tax-code ids, leaving every cached payload
		// identical — only the generation bump makes the till re-ask.
		plugins.SharedBus(db).BumpGeneration()
	}
	return added, false
}

// reconcileTaxDeTakeawayOverridesOnActivate mirrors mergeTakeawayOverrides
// for the OTHER direction of ut-docs#512/#1370: a catalog import can
// happen BEFORE the German tax plugin is installed (tax codes created,
// overrides silently skipped — the ut-docs#531 branch), and nothing
// re-visits those tax codes once the plugin activates later. Reconciles
// from every ACTIVE tax code's pinned takeaway rate (not just the rows one
// import just touched), covering "the plugin was installed/re-enabled
// after the catalog already existed" the way mergeTakeawayOverrides covers
// "the catalog was imported after the plugin already existed". Add-only
// via the same MergeAdditiveJSONMapSetting — a manual override is never
// touched. Safe to call after every activation (install, enable, upgrade)
// of taxDePluginID: idempotent, a shop with nothing new to reconcile is a
// clean no-op (no write, no generation bump).
//
// Product decision (2026-09-01, ut-docs#1370): a successful country-plugin
// install/enable IS the consent boundary — the applicable default legal
// values become ACTIVE overrides immediately, not a placeholder-only
// suggestion the settings screen renders as if it were configured.
func reconcileTaxDeTakeawayOverridesOnActivate(ctx context.Context, db *sql.DB) (added int, failed bool) {
	taxCodes, err := data.NewCatalogRepo(db).ListTaxCodes(ctx)
	if err != nil {
		log.Printf("[plugins] list tax codes for takeaway_rate_overrides reconcile: %v", err)
		return 0, true
	}
	discovered := map[string]int{}
	for _, tc := range taxCodes {
		// > 0: the real plugin treats rate<=0 as "no opinion" (ut-docs#1351),
		// so a zero-pinned entry would be a dead write.
		if tc.TakeawayRateBP != nil && *tc.TakeawayRateBP > 0 {
			discovered[tc.ID] = int(*tc.TakeawayRateBP)
		}
	}
	if len(discovered) == 0 {
		return 0, false
	}
	return mergeTakeawayOverrides(ctx, db, discovered)
}

// reconcileTaxDeTakeawayOverridesIfActivated calls
// reconcileTaxDeTakeawayOverridesOnActivate exactly when pluginID is the
// German tax plugin, logging (never failing the caller) on error. Every
// plugin-activation call site in this package should call this right
// before its own ReloadPlugins — ut-docs#1370's second review round found
// two operator-facing activation paths (import-from-file, Plugin Store
// install) that had been missed because the guard was duplicated per call
// site instead of centralized; a future new activation path now only
// needs to call this one function to be covered.
func reconcileTaxDeTakeawayOverridesIfActivated(ctx context.Context, db *sql.DB, pluginID string) {
	if pluginID != taxDePluginID {
		return
	}
	if _, failed := reconcileTaxDeTakeawayOverridesOnActivate(ctx, db); failed {
		log.Printf("[plugins] reconcile takeaway_rate_overrides on activation of %s: failed, see prior log line", pluginID)
	}
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
	case catimport.BarcodeIssueDuplicateItemNumber:
		return fmt.Sprintf(T("import.status.barcode_duplicate_item_number"), it.BarcodeIssueRaw)
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

// translateSKUIssue is translateBarcodeIssue's counterpart for the
// (non-blocking) SKUIssue reason code (ut-docs#1222).
func translateSKUIssue(T func(string) string, code, raw string) string {
	switch code {
	case catimport.SKUIssueDuplicateInFile:
		return fmt.Sprintf(T("import.status.sku_reused_in_file"), raw)
	default:
		log.Printf("[import] unrecognised SKU issue reason code %q", code)
		return T("import.status.unknown_issue")
	}
}

// translateImageIssue is translateBarcodeIssue's counterpart for the
// (non-blocking) ImageIssue reason codes (ut-docs#1223).
func translateImageIssue(T func(string) string, code, raw string) string {
	switch code {
	case catimport.ImageIssueUnresolved:
		return fmt.Sprintf(T("import.status.image_unresolved"), raw)
	case catimport.ImageIssueTooLarge:
		return fmt.Sprintf(T("import.status.image_too_large"), raw)
	default:
		log.Printf("[import] unrecognised image issue reason code %q", code)
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
