package pages

import (
	"context"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/clock"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5071: a backup that lives only on the till dies with it, and
// nothing copies one off on any plan. Until a manager downloads (or saves a
// copy of) a backup, the status bar reminds them — a chip, never a modal.

const (
	backupCopiedAtKey  = data.BackupCopiedAtSettingsKey
	backupCopySinceKey = data.BackupCopySinceSettingsKey
	// backupCopyMaxAge is how long a till may go without a backup copied
	// off it before the chip shows.
	backupCopyMaxAge = 7 * 24 * time.Hour
)

// backupCopyChipNow is the chip's clock (internal/clock, which docs-shots
// pins); a var so tests can set it.
var backupCopyChipNow = clock.Now

// recordBackupCopied notes that a backup left this till just now.
func recordBackupCopied(ctx context.Context, d *common.Deps) {
	if d.Settings == nil {
		return
	}
	if err := d.Settings.Set(ctx, backupCopiedAtKey, backupCopyChipNow().UTC().Format(time.RFC3339)); err != nil {
		logging.L().Warnf("backup: record the copy: %v", err)
	}
}

// backupCopyDue reports whether no backup was copied off this till within
// backupCopyMaxAge. The clock starts at the first check (a new till is not
// nagged on day one), or at the last copy, whichever is later.
func backupCopyDue(ctx context.Context, d *common.Deps, now time.Time) bool {
	if d.Settings == nil {
		return false
	}
	// GetOrCreate writes once per till (and again after a restore drops
	// the key, db.RestoreDropKeys): the reminder's start. Every later poll
	// only reads. The key is per-till, so the admin sync never carries it.
	since, err := d.Settings.GetOrCreate(ctx, backupCopySinceKey, now.UTC().Format(time.RFC3339))
	if err != nil {
		return false
	}
	ref, err := time.Parse(time.RFC3339, since)
	if err != nil || ref.After(now) {
		// Unreadable, or in the future (a clock that stepped back): start
		// the reminder again from now rather than silence it for good.
		ref = now
		_ = d.Settings.Set(ctx, backupCopySinceKey, now.UTC().Format(time.RFC3339))
	}
	if v, ok, _ := d.Settings.Get(ctx, backupCopiedAtKey); ok {
		// A copy dated in the future is ignored for the same reason.
		if last, err := time.Parse(time.RFC3339, v); err == nil && last.After(ref) && !last.After(now) {
			ref = last
		}
	}
	return now.Sub(ref) >= backupCopyMaxAge
}

// registerBackupCopyChip: GET /ui/backup-copy-chip, polled from the status
// bar (base.html) like /ui/disk-space-chip. Empty 200 unless the viewer may
// manage backups (manager/admin) and a copy is due; then a status chip
// linking to Settings → Backups. It is not one of the chips that bring the
// status bar onto the sale screen (app.css): the sale screen keeps its
// space, and the manager sees it in the back office.
func registerBackupCopyChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/backup-copy-chip", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "data_management") || !backupCopyDue(r.Context(), d, backupCopyChipNow()) {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/backup_copy_chip.html", nil)(w, r)
	})
}
