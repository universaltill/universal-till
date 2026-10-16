package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/catsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// API-source imports (ADR-0161, ut-docs#4429): an import entry declaring
// "source":"api" pulls from another system over its API. POST
// /api/data/import then takes no file, only mode=preview|commit; the
// plugin hands rows back through import_rows_submit during its
// "import.requested.ask", and core applies them with internal/catsync
// once the plugin answers ok. Same gates as a file import: import_export
// (checked by the caller), the resolved entities, main till only for a
// commit, one commit per entry at a time, one audit entry per commit.

// apiImportRequestPayload is what an api-source entry's plugin receives on
// "import.requested.ask" — importRequestPayload without any file field.
type apiImportRequestPayload struct {
	EntryKey string   `json:"entry_key"`
	Entities []string `json:"entities"`
	Mode     string   `json:"mode"`   // preview | commit
	Source   string   `json:"source"` // always "api"
}

// apiImportCommitKey is reserveImportCommit's key for one entry's commits.
func apiImportCommitKey(pluginID, entryKey string) string {
	return "api-import\x00" + pluginID + "\x00" + entryKey
}

// apiImportError is a failed API-source run: the HTTP status and English
// message the JSON API answers with, the locale key the import page shows
// (ut-docs#4431), the plugin's own words where it declined (detail), and
// for a commit that stopped part-way the rows it applied.
type apiImportError struct {
	status  int
	msg     string
	key     string
	detail  string
	applied int
}

// Locale keys of apiImportError.key.
const (
	apiImportErrNoEntities   = "import.system.error.no_entities"
	apiImportErrReplica      = "import.error.replica_use_primary"
	apiImportErrRunning      = "import.error.already_in_progress"
	apiImportErrNoResponse   = "import.system.error.no_response"
	apiImportErrRefused      = "import.system.error.refused"
	apiImportErrBadAnswer    = "import.system.error.bad_answer"
	apiImportErrDeclined     = "import.system.error.declined"
	apiImportErrFailed       = "import.system.error.failed"
	apiImportErrPreview      = "import.system.error.preview_failed"
	apiImportErrPartial      = "import.system.error.partial"
	apiImportErrBadMode      = "import.system.error.failed"
	apiImportErrNotInstalled = "import.system.error.not_found"
)

// apiImportOutcome is a successful run.
type apiImportOutcome struct {
	message      string // the plugin's own message; "" when it gave none
	res          catsync.Result
	pluginCounts map[string]int
}

// runAPISourceImport serves an api-source entry on the JSON API. granted is
// the run's requested ∩ declared ∩ granted "<entity>:write" set.
func runAPISourceImport(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.ImportEntryRow, granted []string, mode string) {
	out, aerr := executeAPISourceImport(r, d, entry, granted, mode)
	if aerr != nil {
		dataAPIRespond(w, aerr.status, false, aerr.msg)
		return
	}
	counts := map[string]int{"new": out.res.New, "updated": out.res.Updated, "unchanged": out.res.Unchanged, "issues": out.res.Issues}
	samples := out.res.Samples
	if samples == nil {
		samples = []catsync.Sample{}
	}
	msg := out.message
	if msg == "" {
		msg = "import plugin accepted the request"
	}
	payload := map[string]any{"message": msg, "mode": mode, "counts": counts, "samples": samples}
	if len(out.pluginCounts) > 0 {
		payload["plugin_counts"] = out.pluginCounts
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": payload, "error": nil})
}

// executeAPISourceImport runs one preview or commit of an api-source entry:
// asks the plugin, collects its rows and applies them with catsync. Shared
// by the JSON API and the import page (ut-docs#4431).
func executeAPISourceImport(r *http.Request, d *common.Deps, entry data.ImportEntryRow, granted []string, mode string) (apiImportOutcome, *apiImportError) {
	fail := func(status int, key, msg string) (apiImportOutcome, *apiImportError) {
		return apiImportOutcome{}, &apiImportError{status: status, key: key, msg: msg}
	}
	if mode != "preview" && mode != "commit" {
		return fail(http.StatusBadRequest, apiImportErrBadMode, "mode must be preview or commit")
	}
	// Core applies items today; stock rides on item rows (ADR-0161).
	var entities []string
	stock := false
	for _, e := range granted {
		switch e {
		case catsync.EntityItems:
			entities = append(entities, e)
		case "stock":
			stock = true
		}
	}
	if len(entities) == 0 {
		return fail(http.StatusBadRequest, apiImportErrNoEntities, "no importable entities — an API-source import applies items, and the entry must declare items and hold items:write")
	}
	if stock {
		entities = append(entities, "stock")
	}
	commit := mode == "commit"
	// Imports are applied on the main till only: an additional till's
	// catalogue comes from the main till's sync and would be reverted.
	if commit && d.SyncPrimaryURL(r.Context()) != "" {
		return fail(http.StatusConflict, apiImportErrReplica, "imports are applied on the main till only")
	}
	if commit {
		key := apiImportCommitKey(entry.PluginID, entry.Key)
		if !reserveImportCommit(key) {
			return fail(http.StatusConflict, apiImportErrRunning, "an import from this source is already running")
		}
		defer releaseImportCommit(key)
	}

	sink := plugins.NewImportRowSink(entry.PluginID, []string{catsync.EntityItems})
	resp, ok, err := plugins.SharedBus(d.Db).AskPlugin(plugins.WithImportRowSink(r.Context(), sink), entry.PluginID, "import.requested.ask",
		apiImportRequestPayload{EntryKey: entry.Key, Entities: entities, Mode: mode, Source: plugins.ImportSourceAPI})
	if err != nil {
		return fail(http.StatusInternalServerError, apiImportErrNoResponse, err.Error())
	}
	if !ok {
		return fail(http.StatusBadRequest, apiImportErrNoResponse, "import plugin did not respond")
	}
	// Over a cap the whole run is refused, whatever the plugin answered.
	if refused := sink.Refused(); refused != nil {
		return fail(http.StatusBadRequest, apiImportErrRefused, refused.Error())
	}
	var parsed importResponse
	if jerr := json.Unmarshal(resp, &parsed); jerr != nil {
		return fail(http.StatusInternalServerError, apiImportErrBadAnswer, "import plugin returned an invalid response")
	}
	if !parsed.OK {
		msg := parsed.Error
		if msg == "" {
			msg = "import plugin declined without a reason"
		}
		return apiImportOutcome{}, &apiImportError{status: http.StatusBadRequest, key: apiImportErrDeclined, msg: msg, detail: parsed.Error}
	}

	opt := catsync.Options{Source: entry.PluginID, Commit: commit, ActorID: getSessionUserID(r), Stock: stock}
	if stock {
		loc, lerr := pos.ResolveStockLocationID(r.Context(), d.Db, tillRegisterIDBestEffort(r.Context(), d))
		if lerr != nil {
			logging.L().Errorf("[import-api] %s: resolve stock location: %v", entry.PluginID, lerr)
			return fail(http.StatusInternalServerError, apiImportErrFailed, "could not resolve the stock location")
		}
		opt.LocationID = loc
	}
	res, err := catsync.Run(r.Context(), d.Db, sink.Rows(catsync.EntityItems), opt)
	// A commit applies in chunks (ADR-0161 §3), so a run that stopped can
	// still have applied rows: those are audited too, flagged partial.
	partial := err != nil && commit && res.New+res.Updated > 0
	if commit && (err == nil || partial) {
		// Plugin, source and counts only — never row contents or credentials.
		if aerr := data.NewPOSRepo(d.Db).InsertAudit(context.WithoutCancel(r.Context()), nil, getSessionUserID(r), "catalog", "-", "import_sync",
			map[string]any{"plugin": entry.PluginID, "entry_key": entry.Key, "source": entry.PluginID,
				"entity": catsync.EntityItems, "rows": sink.Len(), "stock": stock, "partial": partial,
				"new": res.New, "updated": res.Updated, "unchanged": res.Unchanged, "issues": res.Issues},
			time.Now().UTC().Format(time.RFC3339), ""); aerr != nil {
			logging.L().Errorf("[import-api] %s: audit: %v", entry.PluginID, aerr)
		}
	}
	if err != nil {
		logging.L().Errorf("[import-api] %s %s: %v", entry.PluginID, mode, err)
		if partial {
			applied := res.New + res.Updated
			return apiImportOutcome{}, &apiImportError{status: http.StatusInternalServerError, key: apiImportErrPartial, applied: applied,
				msg: fmt.Sprintf("the import stopped part-way after applying %d rows; run it again to finish", applied)}
		}
		if !commit {
			return fail(http.StatusInternalServerError, apiImportErrPreview, "the import preview could not be built")
		}
		return fail(http.StatusInternalServerError, apiImportErrFailed, "the import could not be applied")
	}
	return apiImportOutcome{message: parsed.Message, res: res, pluginCounts: parsed.Counts}, nil
}
