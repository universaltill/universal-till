package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/procrestart"
)

// Seams over internal/procrestart/db so handler tests can observe "a
// restart was scheduled" without a real syscall.Exec replacing the test
// binary mid-run — same hermetic convention pairing_join.go's
// pairingRestartFn/pairingRestartSupported/pairingRestorePending use over
// the same underlying packages (ut-docs#1550). Named separately per call
// site rather than shared, matching that file's own precedent.
var (
	backupRestartFn        = procrestart.Restart
	backupRestartSupported = procrestart.Supported
	backupRestorePending   = db.PendingRestore
)

// backupUIRow is one snapshot row on the settings page.
type backupUIRow struct {
	Name string
	Size string
	Date string
}

// listBackupsForUI formats the snapshot list for the settings card. Date is
// locale-aware (ut-docs#1936) via httpx.FormatDateTime, the same func the
// Data card's reset-batches list next to it already used before this fix —
// this table had been the one site #1894 missed.
func listBackupsForUI(d *common.Deps, locale string) []backupUIRow {
	list, err := db.ListBackups(d.Cfg.DBPath)
	if err != nil {
		return nil
	}
	var out []backupUIRow
	for _, b := range list {
		out = append(out, backupUIRow{
			Name: b.Name,
			Size: fmt.Sprintf("%.1f MB", float64(b.Size)/(1<<20)),
			Date: httpx.FormatDateTime(b.ModTime.Local(), locale),
		})
	}
	return out
}

// saveBackupToDownloads copies a backup file into the user's Downloads folder
// and returns the destination path. Used by the desktop app, whose WebView
// can't download an HTTP attachment.
func saveBackupToDownloads(src, name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return copyBackupTo(filepath.Join(home, "Downloads"), src, name)
}

// copyBackupTo copies src into dstDir/name (creating dstDir). Split out so it's
// testable without writing to the real Downloads folder.
func copyBackupTo(dstDir, src, name string) (string, error) {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dstDir, name)
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	return dst, nil
}

// snapshotWithAssets is a package var so tests can inject a photos failure.
var snapshotWithAssets = db.SnapshotWithAssets

// registerBackupAPI mounts local backup & restore (docs:
// architecture/local-backup.md). Manager/admin only throughout.
func registerBackupAPI(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	dbPath := d.Cfg.DBPath

	audit := func(r *http.Request, action string, payload map[string]any) {
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "backup", "-", action,
			payload, time.Now().UTC().Format(time.RFC3339), "")
	}
	deny := func(w http.ResponseWriter, r *http.Request) bool {
		if canPerform(d, r, "data_management") {
			return false
		}
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
		return true
	}

	mux.HandleFunc("POST /api/backup/now", func(w http.ResponseWriter, r *http.Request) {
		// Mutating + audit-writing (ut-docs#557): a denied session gets an
		// in-place PIN re-auth instead of a flat 403. Only this endpoint —
		// backup/download, /save-copy and /restore below stay on the plain
		// deny() 403 gate for now (see the Dev report for why).
		elev := checkOrElevate(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/backup/now", "#backup-msg",
				httpx.T(locale, "elevation.summary.backup_now"), nil, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		auditNow := func(action string, payload map[string]any) {
			now := time.Now().UTC().Format(time.RFC3339)
			if elev.Outcome == elevated {
				_ = posRepo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "backup", "-", action, payload, now, "")
				return
			}
			_ = posRepo.InsertAudit(r.Context(), nil, actorID, "backup", "-", action, payload, now, "")
		}

		// Uploaded photos ride inside the snapshot (ut-docs#2724).
		path, err := snapshotWithAssets(d.Db, dbPath, paths.Data("public", "assets"))
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil && path == "" {
			// Raw err.Error() here is intentional, not a leak (ut-docs#947
			// Problem 1): the audit log is a manager/admin diagnostic
			// surface, not the operator-facing screen LogAndLocalizedError
			// exists to protect — an admin reviewing a failed backup needs
			// the real error, not a translated summary.
			auditNow("backup_failed", map[string]any{"error": err.Error()})
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, template.HTMLEscapeString(httpx.T(locale, "settings.backup.failed")))
			return
		}
		_ = db.PruneBackups(dbPath, db.DefaultBackupKeep)
		created := map[string]any{"file": filepath.Base(path)}
		if err != nil {
			// The DB snapshot is written and kept; only its photos are
			// missing, which the audit row records for the admin.
			created["photos_error"] = err.Error()
		}
		auditNow("backup_created", created)
		// The Backup card refreshes itself so the list shows the new
		// snapshot (ut-docs#2904): settings.html's button keys
		// "ut-ok refresh-region" on this header, and the elevation dialog's
		// elevation-done step does the same on the approved retry.
		w.Header().Set("X-UT-Response", "ok")
		if err != nil {
			// Snapshot kept, photos missing: warn instead of the tick so an
			// operator about to wipe the device is not told all is well
			// (ut-docs#3948).
			fmt.Fprintf(w, `<span><span class="row-warn-icon" aria-hidden="true">⚠</span>%s</span>`,
				template.HTMLEscapeString(httpx.T(locale, "settings.backup.done_no_photos")))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, template.HTMLEscapeString(httpx.T(locale, "settings.backup.done")))
	})

	mux.HandleFunc("GET /api/backup/download/{name}", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		name := r.PathValue("name")
		if !db.ValidBackupName(name) {
			http.Error(w, "invalid backup name", http.StatusBadRequest)
			return
		}
		dir, err := db.BackupDir(dbPath)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.backup.download_failed", "backup_download", err)
			return
		}
		full := filepath.Join(dir, name)
		if _, err := os.Stat(full); err != nil {
			http.Error(w, "backup not found", http.StatusNotFound)
			return
		}
		audit(r, "backup_downloaded", map[string]any{"file": name})
		if r.Method == http.MethodGet { // not a HEAD probe (ut-docs#5071)
			recordBackupCopied(r.Context(), d)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, full)
	})

	// Save a copy of a backup into the user's Downloads folder. The direct
	// download above relies on the browser handling a Content-Disposition
	// attachment; the desktop app's WebView does NOT download attachments, so
	// the link silently did nothing there. This POST works everywhere (htmx),
	// putting the file somewhere the operator can find it on this machine.
	mux.HandleFunc("POST /api/backup/save-copy/{name}", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		name := r.PathValue("name")
		if !db.ValidBackupName(name) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.backup.save_failed"))
			return
		}
		dir, err := db.BackupDir(dbPath)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.backup.save_failed"))
			return
		}
		dst, err := saveBackupToDownloads(filepath.Join(dir, name), name)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.backup.save_failed"))
			return
		}
		audit(r, "backup_saved_copy", map[string]any{"file": name, "dest": dst})
		recordBackupCopied(r.Context(), d) // ut-docs#5071
		fmt.Fprintf(w, `<span>✓ %s <code>%s</code></span>`, httpx.T(locale, "settings.backup.saved_to"), dst)
	})

	mux.HandleFunc("POST /api/backup/restore", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.Form.Get("name"))
		if !db.ValidBackupName(name) {
			http.Error(w, "invalid backup name", http.StatusBadRequest)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		// ut-docs#1860 (ADR-0087): step-up re-authentication replaces the old
		// typed RESTORE word. This is plausibly the single most destructive
		// action in the product — it discards the entire live database — and
		// a typed word is no stronger a barrier than an already-logged-in
		// session. checkStepUp (not checkOrElevate), because the UI reaching
		// this handler is already manager-only ({{ if .isManager }} in
		// settings.html) AND already canPerform-gated by deny() above, so
		// every session that gets here is exactly the one checkOrElevate
		// would wave through with no PIN at all — same shape as
		// data_api.go's four Settings → Data handlers (ut-docs#1841).
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/backup/restore", "#restore-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.backup_restore"), name),
				[]elevationHiddenField{{Name: "name", Value: name}}, elev)
			return
		}
		// auditElev records the audit row inside the SAME dual-attribution
		// shape elevationActors/InsertAuditElevated establish elsewhere
		// (data_api.go, reset_archive_repo.go): the approver as actor once
		// elevated, with the originally-blocked session user carried
		// alongside it.
		auditElev := func(action string, payload map[string]any) {
			actorID, blockedActorID := elevationActors(elev)
			now := time.Now().UTC().Format(time.RFC3339)
			if blockedActorID != "" {
				_ = posRepo.InsertAuditElevated(r.Context(), nil, actorID, blockedActorID, "backup", "-", action, payload, now, "")
				return
			}
			_ = posRepo.InsertAudit(r.Context(), nil, actorID, "backup", "-", action, payload, now, "")
		}
		// ut-docs#5070: back up the current data first, so whatever the
		// restore replaces is one ordinary backup away (besides the
		// pre-restore copy boot sets aside). No snapshot, no restore. A
		// snapshot whose photos could not be added still holds every row.
		if dir, derr := db.BackupDir(dbPath); derr == nil {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				auditElev("restore_stage_failed", map[string]any{"file": name, "error": err.Error()})
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "settings.backup.stage_failed", "backup_restore", err)
				return
			}
		}
		safety, serr := snapshotWithAssets(d.Db, dbPath, paths.Data("public", "assets"))
		if safety == "" {
			auditElev("restore_stage_failed", map[string]any{"file": name, "error": fmt.Sprintf("safety backup: %v", serr)})
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.backup.safety_failed", "backup_restore", serr)
			return
		}
		if err := db.StageRestore(dbPath, name); err != nil {
			// Raw err.Error() here is intentional, not a leak (ut-docs#947
			// Problem 1 — see the same note on "backup_failed" above).
			auditElev("restore_stage_failed", map[string]any{"file": name, "error": err.Error()})
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "settings.backup.stage_failed", "backup_restore", err)
			return
		}
		auditElev("restore_staged", map[string]any{"file": name, "safety_backup": filepath.Base(safety)})
		// After staging, never before: pruning first could remove the very
		// (oldest) backup being restored.
		_ = db.PruneBackups(dbPath, db.DefaultBackupKeep)
		// ut-docs#1613: the old flat "restart the till to finish" text was a
		// dead end identical to pairing_wait.html's pre-#1550 "joined"
		// screen — this reuses the same internal/procrestart mechanism via
		// the partial below, rather than leaving the operator with an
		// instruction they can't act on.
		httpx.RenderPartial("ui/partials/backup_restore_staged.html", map[string]any{
			"restartSupported": backupRestartSupported(),
		})(w, r)
	})

	// ut-docs#5070: "Restore from a file". The upload is checked and kept
	// as an ordinary backup (db.ImportBackupFile: SQLite header, integrity,
	// schema not newer than this build, the same shop, opened through the
	// real boot path), then the answer shows what is in it and a Restore
	// button for exactly that file — the existing step-up restore above.
	// Nothing changes until then. deny() first: a cashier is refused on the
	// server, not only by the hidden form (cashier-is-sale-only).
	mux.HandleFunc("POST /api/backup/upload", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		fail := func(status int, msg string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
			fmt.Fprintf(w, `<p role="alert" data-testid="backup-upload-error"><span class="row-warn-icon" aria-hidden="true">⚠</span>%s</p>`, template.HTMLEscapeString(msg))
		}
		dir, err := db.BackupDir(dbPath)
		if err != nil {
			logging.L().Errorf("[backup_upload] %v", err)
			fail(http.StatusInternalServerError, httpx.T(locale, "settings.backup.upload.error.failed"))
			return
		}
		limit := backupUploadCap(dir)
		if limit <= 0 {
			fail(http.StatusInsufficientStorage, httpx.T(locale, "settings.backup.upload.error.no_space"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit+backupUploadFormOverhead)
		mr, err := r.MultipartReader()
		if err != nil {
			fail(http.StatusBadRequest, httpx.T(locale, "settings.backup.upload.error.no_file"))
			return
		}
		var (
			got     db.ImportedBackup
			haveOne bool
			ierr    error
		)
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				if perr != io.EOF {
					ierr = perr
				}
				break
			}
			if part.FormName() != "file" || haveOne {
				_ = part.Close()
				continue
			}
			haveOne = true
			got, ierr = db.ImportBackupFile(dbPath, part, limit, backupLiveStoreID(r, d))
			_ = part.Close()
			if ierr != nil {
				break
			}
		}
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(ierr, db.ErrBackupTooLarge) || errors.As(ierr, &maxErr):
			fail(http.StatusRequestEntityTooLarge, fmt.Sprintf(httpx.T(locale, "settings.backup.upload.error.too_large"), formatBackupBytes(limit)))
			return
		case errors.Is(ierr, db.ErrBackupTooOld):
			fail(http.StatusConflict, httpx.T(locale, "settings.backup.upload.error.too_old"))
			return
		case errors.Is(ierr, db.ErrBackupNewer):
			fail(http.StatusConflict, httpx.T(locale, "settings.backup.upload.error.newer"))
			return
		case errors.Is(ierr, db.ErrBackupOtherShop):
			fail(http.StatusConflict, httpx.T(locale, "settings.backup.upload.error.other_shop"))
			return
		case errors.Is(ierr, db.ErrNotTillBackup):
			logging.L().Infof("[backup_upload] refused: %v", ierr)
			fail(http.StatusBadRequest, httpx.T(locale, "settings.backup.upload.error.not_backup"))
			return
		case ierr != nil:
			logging.L().Errorf("[backup_upload] %v", ierr)
			fail(http.StatusInternalServerError, httpx.T(locale, "settings.backup.upload.error.failed"))
			return
		case !haveOne:
			fail(http.StatusBadRequest, httpx.T(locale, "settings.backup.upload.error.no_file"))
			return
		}
		audit(r, "backup_uploaded", map[string]any{"file": got.Name, "sales": got.Sales})
		hasSales, _ := posRepo.HasCompletedSales(r.Context())
		newest := ""
		if !got.NewestSale.IsZero() {
			newest = httpx.FormatDateTime(got.NewestSale.Local(), locale)
		}
		httpx.RenderPartial("ui/partials/backup_upload_ready.html", map[string]any{
			"name":         got.Name,
			"shopName":     got.ShopName,
			"sales":        got.Sales,
			"newestSale":   newest,
			"hasLiveSales": hasSales,
		})(w, r)
	})

	// ut-docs#1613: the restore-staged screen's restart trigger, mirroring
	// pairingRestartHandler (ut-docs#1550) exactly — same refuse-unless-
	// staged guard (a denied/anonymous replay must never restart a
	// configured, possibly-in-use till), same procrestart.Restart() call,
	// same { "data": …, "error": null } envelope. Gated by this file's own
	// deny() (data_management), not managerGate — every other backup route
	// in this file already uses it and a restore is manager/admin-only to
	// begin with, so a caller that could reach this route already staged
	// the restore itself.
	mux.HandleFunc("POST /api/backup/restart-now", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if !backupRestorePending(dbPath) {
			locale := httpx.ResolveLocale(w, r)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": httpx.T(locale, "settings.backup.nothing_to_restart")})
			return
		}
		// No audit() call here, unlike every other route in this file
		// (review finding, ut-docs#1613): the audit_log row would live in
		// the very database this restart is about to replace via
		// ApplyPendingRestore — the same reason "restore_staged" above is
		// this destructive action's last recorded audit entry, not this one.
		backupRestartFn()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]bool{"restarting": true}, "error": nil})
	})
}

// maxBackupUploadBytes is the most a "Restore from a file" upload may be
// (ut-docs#5070): far above a real shop's backup with photos, and below
// what a small till's disk can take twice (the upload plus its restore).
const maxBackupUploadBytes int64 = 4 << 30

// backupUploadFormOverhead is the multipart framing allowed on top.
const backupUploadFormOverhead int64 = 1 << 20

// backupUploadCap is the size cap for an upload into dir: at most
// maxBackupUploadBytes and at most a third of the space left above the
// low-disk floor: the restore then copies the file once more and first
// takes a safety snapshot of the current data. 0 = no room. Unknown
// free space (no probe on this platform) leaves the fixed cap. A var so
// tests can pin it.
var backupUploadCap = func(dir string) int64 {
	u, err := diskspace.Probe(dir)
	if err != nil {
		return maxBackupUploadBytes
	}
	floor := diskspace.Floor(u.Total)
	if u.Free <= floor {
		return 0
	}
	return min(maxBackupUploadBytes, int64((u.Free-floor)/3))
}

// formatBackupBytes renders a size cap for the too-large message.
func formatBackupBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
}

// backupLiveStoreID is the my. shop this till is paired with ("" when
// none): an uploaded backup naming another shop is refused.
func backupLiveStoreID(r *http.Request, d *common.Deps) string {
	if d.Settings != nil {
		if v, _, err := d.Settings.Get(r.Context(), "marketplace.store_id"); err == nil && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if d.Cfg != nil {
		return strings.TrimSpace(d.Cfg.Marketplace.StoreID)
	}
	return ""
}
