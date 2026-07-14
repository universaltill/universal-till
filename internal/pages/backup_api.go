package pages

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
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
		if isManagerOrAuthOff(r) {
			return false
		}
		http.Error(w, "manager or admin required", http.StatusForbidden)
		return true
	}

	mux.HandleFunc("POST /api/backup/now", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		path, err := db.Snapshot(d.Db, dbPath)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			audit(r, "backup_failed", map[string]any{"error": err.Error()})
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, httpx.T(locale, "settings.backup.failed"))
			return
		}
		_ = db.PruneBackups(dbPath, 14)
		audit(r, "backup_created", map[string]any{"file": filepath.Base(path)})
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
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
			audit(r, "restore_stage_failed", map[string]any{"file": name, "error": err.Error()})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		audit(r, "restore_staged", map[string]any{"file": name})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "settings.backup.staged"))
	})
}
