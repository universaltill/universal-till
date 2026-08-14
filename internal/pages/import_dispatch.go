package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// importFormOverhead is the slack allowed on top of maxImportFileSize for
// the multipart framing and the small entry_key/entities form fields when
// bounding the whole request body up front (http.MaxBytesReader) — the
// per-file cap below stays the real limit.
const importFormOverhead = 1 << 20 // 1MB

// registerImportDispatch wires POST /api/data/import (ut-docs#599): the
// host-side trigger that hands an uploaded file to a SPECIFIC installed
// import-type plugin entry. Structurally the mirror of /api/data/export
// (data_api.go) — same manager gate, same entry_key resolution, same
// AskPlugin-never-broadcast rule — but the payload carries a staged-file
// HANDLE instead of inline data: the upload is streamed to a temp file
// (never buffered whole in memory, size-capped on bytes actually written,
// mirroring catimport.ParseBkp's shape) and registered in the plugins
// package's staged-file registry; the guest then pulls the bytes itself
// through the import_file_* host functions (ADR-0001 amendment
// 2026-08-12). Today's hardcoded core importer (POST /api/import,
// import_page.go) is untouched by this.
func registerImportDispatch(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/data/import", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "import_export") {
			dataAPIRespond(w, http.StatusForbidden, false, "manager only")
			return
		}
		// Bound the whole request body BEFORE the multipart parse can spool
		// an arbitrarily large upload anywhere — the cheapest of the three
		// rejection layers (body bound, declared-size fast reject, streamed
		// byte count), so a 5GB upload never fully lands on disk first.
		r.Body = http.MaxBytesReader(w, r.Body, maxImportFileSize+importFormOverhead)
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				dataAPIRespond(w, http.StatusBadRequest, false, fmt.Sprintf("uploaded file exceeds the %d-byte maximum", maxImportFileSize))
				return
			}
			dataAPIRespond(w, http.StatusBadRequest, false, "invalid upload")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			dataAPIRespond(w, http.StatusBadRequest, false, "file required")
			return
		}
		defer file.Close()
		// Declared-size fast reject (same shape as ParseBkp's, ut-docs#594):
		// net/http fills header.Size from the bytes it actually spooled, so
		// this is cheap and reliable; the streamed byte count below stays
		// the authoritative check regardless.
		if header.Size > maxImportFileSize {
			dataAPIRespond(w, http.StatusBadRequest, false, fmt.Sprintf("uploaded file exceeds the %d-byte maximum", maxImportFileSize))
			return
		}

		entries, err := data.NewPluginRepo(d.Db).ListImportEntries(r.Context())
		if err != nil {
			dataAPIRespond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		if len(entries) == 0 {
			dataAPIRespond(w, http.StatusBadRequest, false, "no import plugin installed")
			return
		}

		// entry_key resolution — identical to export's four cases.
		entryKey := strings.TrimSpace(r.FormValue("entry_key"))
		var entry data.ImportEntryRow
		switch {
		case entryKey != "":
			found := false
			for _, e := range entries {
				if e.Key == entryKey {
					entry = e
					found = true
					break
				}
			}
			if !found {
				dataAPIRespond(w, http.StatusNotFound, false, "no installed import entry with that key")
				return
			}
		case len(entries) == 1:
			entry = entries[0]
		default:
			dataAPIRespond(w, http.StatusBadRequest, false, "multiple import entries installed — specify entry_key")
			return
		}

		// Resolve which entities this run covers: the caller's requested
		// list (comma-separated), defaulting to everything the entry
		// declares. Each candidate must be BOTH declared in the entry's
		// manifest AND granted "<entity>:write" (the same per-ledger shape
		// export uses for sales:read/inventory:read, ut-docs#228): an
		// undeclared or ungranted entity is silently omitted rather than
		// failing the whole request — unless nothing at all survives, which
		// IS an invalid request. CheckPermissionGranted (not
		// CheckPermission) so a genuine infrastructure failure still 500s
		// instead of masquerading as a denial.
		declared := map[string]bool{}
		for _, e := range entry.Entities {
			declared[e] = true
		}
		requested := entry.Entities
		if raw := strings.TrimSpace(r.FormValue("entities")); raw != "" {
			// Dedupe as parsed (review finding F4, ut-docs#599): an
			// unbounded, unduplicated "entities" value would otherwise cost
			// one CheckPermissionGranted DB round-trip AND one duplicate
			// entry in the dispatched payload per repeat -- manager-gated,
			// so low severity, but free to close.
			seen := map[string]bool{}
			requested = nil
			for _, e := range strings.Split(raw, ",") {
				if e = strings.TrimSpace(e); e != "" && !seen[e] {
					seen[e] = true
					requested = append(requested, e)
				}
			}
		}
		var granted []string
		for _, e := range requested {
			if !declared[e] {
				continue
			}
			ok, perr := plugins.CheckPermissionGranted(r.Context(), d.Db, entry.PluginID, e+":write")
			if perr != nil {
				dataAPIRespond(w, http.StatusInternalServerError, false, perr.Error())
				return
			}
			if ok {
				granted = append(granted, e)
			}
		}
		if len(granted) == 0 {
			dataAPIRespond(w, http.StatusBadRequest, false, "no importable entities — none of the requested entities are both declared by the import entry and granted the matching :write permission")
			return
		}

		// Stage the upload to a temp file via the streaming-copy pattern
		// catimport.ParseBkp proved (ut-docs#594): io.Copy through a
		// LimitReader against os.CreateTemp — never buffered whole in
		// memory, cap enforced on bytes ACTUALLY WRITTEN as the
		// authoritative check behind the two cheap rejects above.
		tmp, err := os.CreateTemp("", "ut-import-*.upload")
		if err != nil {
			dataAPIRespond(w, http.StatusInternalServerError, false, "could not stage the uploaded file")
			return
		}
		tmpPath := tmp.Name()
		written, copyErr := io.Copy(tmp, io.LimitReader(file, maxImportFileSize+1))
		closeErr := tmp.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(tmpPath)
			logging.L().Errorf("[import-dispatch] stage upload: copy=%v close=%v", copyErr, closeErr)
			dataAPIRespond(w, http.StatusInternalServerError, false, "could not stage the uploaded file")
			return
		}
		if written > maxImportFileSize {
			_ = os.Remove(tmpPath)
			dataAPIRespond(w, http.StatusBadRequest, false, fmt.Sprintf("uploaded file exceeds the %d-byte maximum", maxImportFileSize))
			return
		}

		handle, err := plugins.OpenImportFile(entry.PluginID, tmpPath)
		if err != nil {
			_ = os.Remove(tmpPath)
			logging.L().Errorf("[import-dispatch] register staged file: %v", err)
			dataAPIRespond(w, http.StatusInternalServerError, false, "could not stage the uploaded file")
			return
		}
		// The registry owns the temp file from here. Always release the
		// handle once the ask returns — success, decline, error or timeout:
		// a plugin that never calls import_file_close must not leak the
		// temp file, and the registry's Close is idempotent so this is safe
		// alongside a well-behaved guest's own close.
		defer plugins.CloseImportFile(entry.PluginID, handle)

		// AskPlugin, not Ask — same never-answer-for-another-plugin rule as
		// export (ut-docs#189 review).
		resp, ok, err := plugins.SharedBus(d.Db).AskPlugin(r.Context(), entry.PluginID, "import.requested.ask", importRequestPayload{
			EntryKey:   entry.Key,
			Entities:   granted,
			FileHandle: handle,
			FileName:   filepath.Base(header.Filename),
			FileSize:   written,
		})
		if err != nil {
			dataAPIRespond(w, http.StatusInternalServerError, false, err.Error())
			return
		}
		if !ok {
			dataAPIRespond(w, http.StatusBadRequest, false, "import plugin did not respond")
			return
		}

		var parsed importResponse
		if jerr := json.Unmarshal(resp, &parsed); jerr != nil {
			dataAPIRespond(w, http.StatusInternalServerError, false, "import plugin returned an invalid response")
			return
		}
		if !parsed.OK {
			errMsg := parsed.Error
			if errMsg == "" {
				errMsg = "import plugin declined without a reason"
			}
			dataAPIRespond(w, http.StatusBadRequest, false, errMsg)
			return
		}
		msg := parsed.Message
		if msg == "" {
			msg = "import plugin accepted the request"
		}
		// Same envelope dataAPIRespond writes, plus the per-entity counts
		// when the plugin reported them.
		payload := map[string]any{"message": msg}
		if len(parsed.Counts) > 0 {
			payload["counts"] = parsed.Counts
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": payload, "error": nil})
	})
}
