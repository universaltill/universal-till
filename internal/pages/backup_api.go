package pages

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
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

// listBackupsForUI formats the snapshot list for the settings card.
func listBackupsForUI(d *common.Deps) []backupUIRow {
	list, err := db.ListBackups(d.Cfg.DBPath)
	if err != nil {
		return nil
	}
	var out []backupUIRow
	for _, b := range list {
		out = append(out, backupUIRow{
			Name: b.Name,
			Size: fmt.Sprintf("%.1f MB", float64(b.Size)/(1<<20)),
			Date: b.ModTime.Format("2006-01-02 15:04"),
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

		path, err := db.Snapshot(d.Db, dbPath)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			// Raw err.Error() here is intentional, not a leak (ut-docs#947
			// Problem 1): the audit log is a manager/admin diagnostic
			// surface, not the operator-facing screen LogAndLocalizedError
			// exists to protect — an admin reviewing a failed backup needs
			// the real error, not a translated summary.
			auditNow("backup_failed", map[string]any{"error": err.Error()})
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.backup.failed"))
			return
		}
		_ = db.PruneBackups(dbPath, 14)
		auditNow("backup_created", map[string]any{"file": filepath.Base(path)})
		// Reload so the list shows the new snapshot.
		w.Header().Set("HX-Refresh", "true")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "settings.backup.done"))
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
		fmt.Fprintf(w, `<span>✓ %s <code>%s</code></span>`, httpx.T(locale, "settings.backup.saved_to"), dst)
	})

	mux.HandleFunc("POST /api/backup/restore", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.Form.Get("name"))
		locale := httpx.ResolveLocale(w, r)
		// Destructive: the form must carry the literal confirmation word.
		if strings.ToUpper(strings.TrimSpace(r.Form.Get("confirm"))) != "RESTORE" {
			http.Error(w, httpx.T(locale, "settings.backup.confirm_required"), http.StatusBadRequest)
			return
		}
		if err := db.StageRestore(dbPath, name); err != nil {
			// Raw err.Error() here is intentional, not a leak (ut-docs#947
			// Problem 1 — see the same note on "backup_failed" above).
			audit(r, "restore_stage_failed", map[string]any{"file": name, "error": err.Error()})
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "settings.backup.stage_failed", "backup_restore", err)
			return
		}
		audit(r, "restore_staged", map[string]any{"file": name})
		// ut-docs#1613: the old flat "restart the till to finish" text was a
		// dead end identical to pairing_wait.html's pre-#1550 "joined"
		// screen — this reuses the same internal/procrestart mechanism via
		// the partial below, rather than leaving the operator with an
		// instruction they can't act on.
		httpx.RenderPartial("ui/partials/backup_restore_staged.html", map[string]any{
			"restartSupported": backupRestartSupported(),
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
