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
		// Bound the whole request body BEFORE anything reads it — the
		// cheapest of the two remaining rejection layers (body bound,
		// streamed byte count), so a 5GB upload never fully lands on disk
		// first.
		r.Body = http.MaxBytesReader(w, r.Body, maxImportFileSize+importFormOverhead)

		// Stream the multipart body directly via MultipartReader instead of
		// ParseMultipartForm (ut-docs#613): ParseMultipartForm spools the
		// whole upload to memory or its own "multipart-*" temp file FIRST,
		// and the staging copy further down used to make a SECOND full
		// copy into this handler's own "ut-import-*" file — up to 2x the
		// upload's size on disk (or RAM, if TMPDIR is tmpfs) on the
		// low-power till hardware this targets. Reading parts directly and
		// copying the file part straight into the staged temp file keeps
		// it a single copy. Field order on the wire is caller-controlled
		// (the file field is typically written before entry_key/entities,
		// see postImportUpload), so the small form fields are buffered as
		// they're encountered and every validation below still runs only
		// once every part has been read — same order of checks as before.
		mr, err := r.MultipartReader()
		if err != nil {
			dataAPIRespond(w, http.StatusBadRequest, false, "invalid upload")
			return
		}

		var (
			tmpPath     string
			fileName    string
			written     int64
			haveFile    bool
			entryKeyRaw string
			entitiesRaw string
		)
		// staged tracks whether the temp file's ownership has passed to the
		// plugin registry (OpenImportFile) — mirrors the handle-ownership
		// comment further down. Anything staged here that never reaches
		// that point must be removed by us.
		staged := false
		defer func() {
			if tmpPath != "" && !staged {
				_ = os.Remove(tmpPath)
			}
		}()

		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				var tooBig *http.MaxBytesError
				if errors.As(perr, &tooBig) {
					dataAPIRespond(w, http.StatusBadRequest, false, fmt.Sprintf("uploaded file exceeds the %d-byte maximum", maxImportFileSize))
					return
				}
				dataAPIRespond(w, http.StatusBadRequest, false, "invalid upload")
				return
			}

			switch {
			case part.FormName() == "file" && part.FileName() != "":
				// A second "file" part would silently overwrite tmpPath
				// and orphan the first staged copy — the cleanup defer
				// only ever removes the LAST tmpPath, so the first one
				// would leak forever (review finding, ut-docs#613).
				// Reject outright instead of accepting either one.
				if haveFile {
					_ = part.Close()
					dataAPIRespond(w, http.StatusBadRequest, false, "invalid upload")
					return
				}
				haveFile = true
				fileName = part.FileName()
				// Streaming-copy pattern catimport.ParseBkp proved
				// (ut-docs#594): io.Copy through a LimitReader straight
				// into the staged file — never buffered whole in memory,
				// cap enforced on bytes ACTUALLY WRITTEN as the
				// authoritative check (MultipartReader gives no reliable
				// declared per-part size to fast-reject on first, unlike
				// FormFile's header.Size).
				tmp, cerr := os.CreateTemp("", "ut-import-*.upload")
				if cerr != nil {
					_ = part.Close()
					dataAPIRespond(w, http.StatusInternalServerError, false, "could not stage the uploaded file")
					return
				}
				tmpPath = tmp.Name()
				var copyErr error
				written, copyErr = io.Copy(tmp, io.LimitReader(part, maxImportFileSize+1))
				closeErr := tmp.Close()
				_ = part.Close()
				if copyErr != nil || closeErr != nil {
					logging.L().Errorf("[import-dispatch] stage upload: copy=%v close=%v", copyErr, closeErr)
					dataAPIRespond(w, http.StatusInternalServerError, false, "could not stage the uploaded file")
					return
				}
				if written > maxImportFileSize {
					dataAPIRespond(w, http.StatusBadRequest, false, fmt.Sprintf("uploaded file exceeds the %d-byte maximum", maxImportFileSize))
					return
				}
			case part.FormName() == "entry_key" || part.FormName() == "entities":
				name := part.FormName()
				buf, rerr := io.ReadAll(io.LimitReader(part, importFormOverhead))
				_ = part.Close()
				if rerr != nil {
					dataAPIRespond(w, http.StatusBadRequest, false, "invalid upload")
					return
				}
				if name == "entry_key" {
					entryKeyRaw = string(buf)
				} else {
					entitiesRaw = string(buf)
				}
			default:
				_ = part.Close()
			}
		}
		if !haveFile {
			dataAPIRespond(w, http.StatusBadRequest, false, "file required")
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
		entryKey := strings.TrimSpace(entryKeyRaw)
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
		if raw := strings.TrimSpace(entitiesRaw); raw != "" {
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

		// The file was already staged to tmpPath while reading the
		// multipart parts above — a single copy, not a second one here.
		handle, err := plugins.OpenImportFile(entry.PluginID, tmpPath)
		if err != nil {
			logging.L().Errorf("[import-dispatch] register staged file: %v", err)
			dataAPIRespond(w, http.StatusInternalServerError, false, "could not stage the uploaded file")
			return
		}
		// The registry owns the temp file from here — flip staged so the
		// top-level defer no longer removes it out from under the
		// registry. Always release the handle once the ask returns —
		// success, decline, error or timeout: a plugin that never calls
		// import_file_close must not leak the temp file, and the
		// registry's Close is idempotent so this is safe alongside a
		// well-behaved guest's own close.
		staged = true
		defer plugins.CloseImportFile(entry.PluginID, handle)

		// AskPlugin, not Ask — same never-answer-for-another-plugin rule as
		// export (ut-docs#189 review).
		resp, ok, err := plugins.SharedBus(d.Db).AskPlugin(r.Context(), entry.PluginID, "import.requested.ask", importRequestPayload{
			EntryKey:   entry.Key,
			Entities:   granted,
			FileHandle: handle,
			FileName:   filepath.Base(fileName),
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
