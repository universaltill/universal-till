package pages

import (
	"net/http"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#3121: the status bar tells a manager when the till's disk is below
// the low-disk floor. Housekeeping has already pruned every kind of file to
// its minimum by then (internal/server runHousekeeping); what is left needs
// a person to free space on the device.

// diskSpaceChipProbe is the chip's free-space source; a var so tests can
// pin it.
var diskSpaceChipProbe = diskspace.Probe

// diskSpaceDataDir is the directory whose disk the chip measures: the one
// holding the live database (and its backups/).
func diskSpaceDataDir(d *common.Deps) string {
	if d.Cfg != nil && d.Cfg.DBPath != "" {
		return filepath.Dir(d.Cfg.DBPath)
	}
	return paths.DataDir()
}

// registerDiskSpaceChip: GET /ui/disk-space-chip, polled from the status
// bar on every page (base.html), same shape as /ui/report-archive-chip.
// Empty 200 unless the disk is below the floor and the viewer may open
// Settings (admin/manager); then a status chip — never a modal, selling
// carries on — linking to Settings → Backups. Unknown free space (no probe
// on this platform, or a failed one) shows nothing.
func registerDiskSpaceChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/disk-space-chip", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			w.WriteHeader(http.StatusOK)
			return
		}
		u, err := diskSpaceChipProbe(diskSpaceDataDir(d))
		if err != nil || !diskspace.Low(u) {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/disk_space_chip.html", nil)(w, r)
	})
}
