package pages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/stagedupload"
)

// File-rows import entries (ADR-0164 §2, ut-docs#4526). An import entry
// declaring "source":"file" is a vendor export parser in its plugin: it
// reads the uploaded file through import_file_* and hands ADR-0051 item
// rows back through import_rows_submit, in the import sandbox (no network,
// no settings or secrets — plugins.FileImportHostFunctions). Core applies
// the rows with catsync exactly as for an API-source entry (ADR-0161):
// mode=preview writes nothing, mode=commit links each row under the
// plugin's id. Reached two ways:
//
//   - POST /api/data/import with entry_key, mode and the file (JSON API);
//   - the import page: a file uploaded to POST /api/import that an entry
//     claims (its extension is one of the entry's file_formats and its
//     header row holds every file_headers name — catimport.HeadersClaim,
//     the same check ut-cloud's import job makes) gets the plugin's preview
//     instead of the standard one, and its Import button posts the staged
//     copy to POST /api/import/from-file. "Use the standard importer"
//     resubmits the form with importer=core. A file two or more entries
//     claim, or one over the plugin file cap, stays with the standard
//     importer (claimingFileRowsEntry).
//
// Same gates as every import: import_export (a cashier gets neither the
// preview nor the endpoint, ut-docs#3079), main till only for a commit, one
// commit per entry at a time, one audit entry per commit. Core names no
// vendor: what an entry claims is its own manifest's declaration.

// fileRowsMaxBytes caps a file-rows entry's file (plugins.ImportFileRowsMaxBytes;
// a var so a test can lower it).
var fileRowsMaxBytes = plugins.ImportFileRowsMaxBytes

// fileRowsErrTooLarge is the locale key of an over-cap file.
const fileRowsErrTooLarge = "import.file_plugin.too_large"

// fileRowsRequestPayload is a file-rows entry's "import.requested.ask":
// importRequestPayload plus the run's mode and source "file".
type fileRowsRequestPayload struct {
	EntryKey   string   `json:"entry_key"`
	Entities   []string `json:"entities"`
	FileHandle int32    `json:"file_handle"`
	FileName   string   `json:"file_name"`
	FileSize   int64    `json:"file_size"`
	Mode       string   `json:"mode"`   // preview | commit
	Source     string   `json:"source"` // always "file"
}

// rowsImportFile is a file-rows run's staged upload. executeRowsImport owns
// path: it removes it, or hands it to the plugin file registry, which does.
type rowsImportFile struct {
	path, name string
	size       int64
	sha256     string // set for a commit (the audit names the file by it)
	registered bool   // the plugin file registry owns path
	// keepIfBusy: a commit refused because another commit of the entry is
	// running leaves path in place and sets busy, so the caller can hand a
	// staged copy back for the operator's retry (a double-tap's 409 must
	// not consume it — the standard importer's preserveStaged).
	keepIfBusy, busy bool
}

// fileSHA256 hashes the staged file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stageFileRowsCopy copies file (from offset 0) into a fresh staged temp
// file for one run; the caller owns the returned path.
func stageFileRowsCopy(file io.ReadSeeker) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", stagedupload.ImportPattern)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(tmp, io.LimitReader(file, fileRowsMaxBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp.Name())
		if copyErr != nil {
			return "", copyErr
		}
		return "", closeErr
	}
	return tmp.Name(), nil
}

// stagedOwner is the staging-registry owner of a copy a file-rows entry's
// preview staged: only that entry's commit, by the operator who previewed
// (the session user), may take it.
func stagedOwner(r *http.Request, e data.ImportEntryRow) string {
	return "file-rows\x00" + e.PluginID + "\x00" + e.Key + "\x00" + getSessionUserID(r)
}

// fileRowsEntry finds the installed file-rows entry (pluginID, key).
func fileRowsEntry(ctx context.Context, d *common.Deps, pluginID, key string) (data.ImportEntryRow, bool, error) {
	entries, err := data.NewPluginRepo(d.Db).ListImportEntries(ctx)
	if err != nil {
		return data.ImportEntryRow{}, false, err
	}
	for _, e := range entries {
		if e.PluginID == pluginID && e.Key == key && e.Source == plugins.ImportSourceFile {
			return e, true, nil
		}
	}
	return data.ImportEntryRow{}, false, nil
}

// claimingFileRowsEntry is the installed file-rows entry that claims the
// upload: fileName's extension is one of its file_formats and, when it
// declares file_headers, the file's header row holds them all. The header
// row is read once, only when some entry's extension matched, and never
// for a file over the plugin file cap (fileRowsMaxBytes) — no plugin could
// read it, so the standard importer takes it. A read failure claims
// nothing (the standard importer then reports the file as it always has).
//
// Exactly one claiming entry is required: when two or more claim the file
// the till does not pick one by install order — the standard importer
// takes it, and the data API (entry_key) still reaches each plugin. Every
// claim names a column specific to its export (validateFileRowsEntry), so
// two claims mean two importers for the same system.
func claimingFileRowsEntry(ctx context.Context, d *common.Deps, fileName string, file io.ReaderAt, size int64) (data.ImportEntryRow, bool) {
	ext := plugins.NormalizeFileFormat(filepath.Ext(fileName))
	if ext == "" || size > fileRowsMaxBytes {
		return data.ImportEntryRow{}, false
	}
	entries, err := data.NewPluginRepo(d.Db).ListImportEntries(ctx)
	if err != nil {
		logging.L().Errorf("[import-file] list import entries: %v", err)
		return data.ImportEntryRow{}, false
	}
	var (
		headers     []string
		headersRead bool
		claims      []data.ImportEntryRow
	)
	for _, e := range entries {
		if e.Source != plugins.ImportSourceFile || !declaresFormat(e.FileFormats, ext) {
			continue
		}
		if len(e.FileHeaders) == 0 {
			claims = append(claims, e)
			continue
		}
		if !headersRead {
			headersRead = true
			if headers, err = catimport.HeaderRow(file, size, fileName); err != nil {
				return data.ImportEntryRow{}, false
			}
		}
		if catimport.HeadersClaim(headers, e.FileHeaders) {
			claims = append(claims, e)
		}
	}
	if len(claims) > 1 {
		logging.L().Warnf("[import-file] %d import plugins claim an upload; using the standard importer", len(claims))
		return data.ImportEntryRow{}, false
	}
	if len(claims) == 1 {
		return claims[0], true
	}
	return data.ImportEntryRow{}, false
}

func declaresFormat(formats []string, ext string) bool {
	for _, f := range formats {
		if plugins.NormalizeFileFormat(f) == ext {
			return true
		}
	}
	return false
}

// importPageFileRows serves an import-page run of a claimed file. A
// preview stages the upload for its commit, runs the plugin's preview on a
// second copy, and renders the result fragment with the staged id. A
// direct commit (Import pressed without a preview) runs the commit on a
// copy. file is the request's upload, read from offset 0.
func importPageFileRows(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.ImportEntryRow, file io.ReadSeeker, fileName string, size int64, commit bool) {
	mode := "preview"
	if commit {
		mode = "commit"
	}
	v := importSystemView{PluginID: entry.PluginID, EntryKey: entry.Key, Mode: mode, File: true, FileName: filepath.Base(fileName), PluginName: entry.PluginName}
	if v.PluginName == "" {
		v.PluginName = entry.PluginID
	}
	granted, err := grantedImportEntities(r.Context(), d, entry, "")
	if err != nil {
		logging.L().Errorf("[import-file] %s: permissions: %v", entry.PluginID, err)
		v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
		renderImportSystemResult(w, r, v)
		return
	}
	stagedID := ""
	if !commit {
		if stagedID, err = stageCatalogUploadFor(file, stagedOwner(r, entry)); err != nil {
			logging.L().Errorf("[import-file] %s: stage upload: %v", entry.PluginID, err)
			v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
			renderImportSystemResult(w, r, v)
			return
		}
	}
	runPath, err := stageFileRowsCopy(file)
	if err != nil {
		if stagedID != "" {
			discardStagedCatalogUploadFor(stagedID, stagedOwner(r, entry))
		}
		logging.L().Errorf("[import-file] %s: copy upload: %v", entry.PluginID, err)
		v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
		renderImportSystemResult(w, r, v)
		return
	}
	out, aerr := executeRowsImport(r, d, entry, granted, mode, &rowsImportFile{path: runPath, name: fileName, size: size})
	if aerr != nil {
		if stagedID != "" {
			discardStagedCatalogUploadFor(stagedID, stagedOwner(r, entry))
		}
		v.Err = aerr
		renderImportSystemResult(w, r, v)
		return
	}
	v.StagedID = stagedID
	v.Message, v.Counts = out.message, out.res
	renderImportSystemResult(w, r, v)
}

// registerImportFromFile wires POST /api/import/from-file: the import
// page's Import for a claimed file. It commits the copy staged at preview
// (single use, like the standard importer's staged commit) through the
// same plugin entry, and answers the result fragment.
func registerImportFromFile(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /api/import/from-file", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "import_export") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, importFormOverhead)
		if err := r.ParseForm(); err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
			return
		}
		pluginID := strings.TrimSpace(r.PostForm.Get("plugin_id"))
		entryKey := strings.TrimSpace(r.PostForm.Get("entry_key"))
		stagedID := strings.TrimSpace(r.PostForm.Get("staged_id"))
		v := importSystemView{PluginID: pluginID, EntryKey: entryKey, Mode: "commit", File: true}
		entry, found, err := fileRowsEntry(r.Context(), d, pluginID, entryKey)
		if err != nil {
			logging.L().Errorf("[import-file] list import entries: %v", err)
			v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
			renderImportSystemResult(w, r, v)
			return
		}
		if !found {
			v.Err = &apiImportError{status: http.StatusNotFound, key: apiImportErrNotInstalled}
			renderImportSystemResult(w, r, v)
			return
		}
		// Checked before the staged copy is taken (executeRowsImport checks
		// again): an additional till refuses without consuming it.
		if d.SyncPrimaryURL(r.Context()) != "" {
			v.Err = &apiImportError{status: http.StatusConflict, key: apiImportErrReplica}
			renderImportSystemResult(w, r, v)
			return
		}
		// Only a copy this entry's own preview staged, for this operator
		// (stagedOwner).
		owner := stagedOwner(r, entry)
		path, ok := takeStagedCatalogUploadFor(stagedID, owner)
		if !ok {
			v.Err = &apiImportError{status: http.StatusBadRequest, key: "import.error.stage_expired"}
			renderImportSystemResult(w, r, v)
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			_ = os.Remove(path)
			v.Err = &apiImportError{status: http.StatusBadRequest, key: "import.error.stage_expired"}
			renderImportSystemResult(w, r, v)
			return
		}
		granted, err := grantedImportEntities(r.Context(), d, entry, "")
		if err != nil {
			_ = os.Remove(path)
			logging.L().Errorf("[import-file] %s: permissions: %v", pluginID, err)
			v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
			renderImportSystemResult(w, r, v)
			return
		}
		// The name the operator uploaded (the plugin may read its
		// extension); informational only, never a path.
		name := filepath.Base(strings.TrimSpace(r.PostForm.Get("file_name")))
		if len(name) > 255 || name == "." || name == string(filepath.Separator) {
			name = ""
		}
		run := &rowsImportFile{path: path, name: name, size: info.Size(), keepIfBusy: true}
		out, aerr := executeRowsImport(r, d, entry, granted, "commit", run)
		if run.busy {
			// Another commit of this entry is running (a double-tap): the
			// copy goes back for the operator's retry.
			restageCatalogUploadFor(stagedID, path, owner)
		}
		if aerr != nil {
			v.Err = aerr
			renderImportSystemResult(w, r, v)
			return
		}
		v.Message, v.Counts = out.message, out.res
		renderImportSystemResult(w, r, v)
	})
}

// useStandardImporter: the operator asked for the standard importer on a
// file a plugin claims (the preview's "Use the standard importer").
func useStandardImporter(r *http.Request) bool {
	return r.FormValue("importer") == "core"
}
