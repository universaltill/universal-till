package pages

import (
	"encoding/json"
	"net/http"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
)

// registerUpdateAPI exposes the manager-gated in-app updater. It downloads the
// latest release, verifies its checksum, swaps the binary + web assets, and
// re-execs (archive installs only; the .deb/Windows use their native updaters).
func registerUpdateAPI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/update/apply", func(w http.ResponseWriter, r *http.Request) {
		if !isManagerOrAuthOff(r) {
			http.Error(w, "manager only", http.StatusForbidden)
			return
		}
		respond := func(status int, ok bool, msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "message": msg})
		}
		if !selfupdate.Supported() {
			respond(http.StatusBadRequest, false, selfupdate.ErrUnsupported.Error())
			return
		}
		// Apply stages the swap and schedules the re-exec; the response flushes
		// before the process restarts.
		if err := selfupdate.Apply(r.Context()); err != nil {
			respond(http.StatusBadGateway, false, err.Error())
			return
		}
		respond(http.StatusOK, true, "update installed — restarting")
	})
}
