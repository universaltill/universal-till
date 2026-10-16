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

// runAPISourceImport serves an api-source entry. granted is the run's
// requested ∩ declared ∩ granted "<entity>:write" set.
func runAPISourceImport(w http.ResponseWriter, r *http.Request, d *common.Deps, entry data.ImportEntryRow, granted []string, mode string) {
	if mode != "preview" && mode != "commit" {
		dataAPIRespond(w, http.StatusBadRequest, false, "mode must be preview or commit")
		return
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
		dataAPIRespond(w, http.StatusBadRequest, false, "no importable entities — an API-source import applies items, and the entry must declare items and hold items:write")
		return
	}
	if stock {
		entities = append(entities, "stock")
	}
	commit := mode == "commit"
	// Imports are applied on the main till only: an additional till's
	// catalogue comes from the main till's sync and would be reverted.
	if commit && d.SyncPrimaryURL(r.Context()) != "" {
		dataAPIRespond(w, http.StatusConflict, false, "imports are applied on the main till only")
		return
	}
	if commit {
		key := apiImportCommitKey(entry.PluginID, entry.Key)
		if !reserveImportCommit(key) {
			dataAPIRespond(w, http.StatusConflict, false, "an import from this source is already running")
			return
		}
		defer releaseImportCommit(key)
	}

	sink := plugins.NewImportRowSink(entry.PluginID, []string{catsync.EntityItems})
	resp, ok, err := plugins.SharedBus(d.Db).AskPlugin(plugins.WithImportRowSink(r.Context(), sink), entry.PluginID, "import.requested.ask",
		apiImportRequestPayload{EntryKey: entry.Key, Entities: entities, Mode: mode, Source: plugins.ImportSourceAPI})
	if err != nil {
		dataAPIRespond(w, http.StatusInternalServerError, false, err.Error())
		return
	}
	if !ok {
		dataAPIRespond(w, http.StatusBadRequest, false, "import plugin did not respond")
		return
	}
	// Over a cap the whole run is refused, whatever the plugin answered.
	if refused := sink.Refused(); refused != nil {
		dataAPIRespond(w, http.StatusBadRequest, false, refused.Error())
		return
	}
	var parsed importResponse
	if jerr := json.Unmarshal(resp, &parsed); jerr != nil {
		dataAPIRespond(w, http.StatusInternalServerError, false, "import plugin returned an invalid response")
		return
	}
	if !parsed.OK {
		msg := parsed.Error
		if msg == "" {
			msg = "import plugin declined without a reason"
		}
		dataAPIRespond(w, http.StatusBadRequest, false, msg)
		return
	}

	opt := catsync.Options{Source: entry.PluginID, Commit: commit, ActorID: getSessionUserID(r), Stock: stock}
	if stock {
		loc, lerr := pos.ResolveStockLocationID(r.Context(), d.Db, tillRegisterIDBestEffort(r.Context(), d))
		if lerr != nil {
			logging.L().Errorf("[import-api] %s: resolve stock location: %v", entry.PluginID, lerr)
			dataAPIRespond(w, http.StatusInternalServerError, false, "could not resolve the stock location")
			return
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
		msg := "the import could not be applied"
		if !commit {
			msg = "the import preview could not be built"
		}
		if partial {
			msg = fmt.Sprintf("the import stopped part-way after applying %d rows; run it again to finish", res.New+res.Updated)
		}
		dataAPIRespond(w, http.StatusInternalServerError, false, msg)
		return
	}
	counts := map[string]int{"new": res.New, "updated": res.Updated, "unchanged": res.Unchanged, "issues": res.Issues}
	msg := parsed.Message
	if msg == "" {
		msg = "import plugin accepted the request"
	}
	samples := res.Samples
	if samples == nil {
		samples = []catsync.Sample{}
	}
	payload := map[string]any{"message": msg, "mode": mode, "counts": counts, "samples": samples}
	if len(parsed.Counts) > 0 {
		payload["plugin_counts"] = parsed.Counts
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": payload, "error": nil})
}
