package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/universaltill/universal-till/internal/catsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// "From another till system" (ut-docs#4431): the import page's section for
// API-source import entries (ADR-0161) — how a till-only shop pulls its
// catalogue straight from its old system. Core stays vendor-neutral: the
// list is whatever installed import entries declare "source":"api", named
// by their plugin. Credentials are the plugin's own secret settings
// (ADR-0082), edited in the existing plugin settings editor — this page
// only says whether each is set, never its value. Same import_export gate
// as the rest of the import page; a cashier gets neither the section nor
// the endpoint (ut-docs#3079).

// importSystemSecret is one secret setting's state, never its value.
type importSystemSecret struct {
	Key, Label string
	Set        bool
}

// importSystemEntry is one installed API importer on the import page.
type importSystemEntry struct {
	PluginID, Key, Name string
	Secrets             []importSystemSecret
}

// importSystemEntries lists the installed api-source import entries, named
// by their plugin, each with its secret settings' set / not-set state.
func importSystemEntries(ctx context.Context, d *common.Deps, locale string) ([]importSystemEntry, error) {
	repo := data.NewPluginRepo(d.Db)
	entries, err := repo.ListImportEntries(ctx)
	if err != nil {
		return nil, err
	}
	var out []importSystemEntry
	for _, e := range entries {
		if e.Source != plugins.ImportSourceAPI {
			continue
		}
		name := e.PluginName
		if name == "" {
			name = e.PluginID
		}
		v := importSystemEntry{PluginID: e.PluginID, Key: e.Key, Name: name}
		rows, err := repo.ListPluginSettings(ctx, e.PluginID)
		if err != nil {
			return nil, err
		}
		// One manifest read serves both the secret test and the labels.
		manifest := settingsFormManifest(ctx, d, e.PluginID)
		isSecret := func(key string) bool {
			return plugins.IsSecretSettingKey(key) || manifest.SettingDeclaredSecret(key)
		}
		type ordered struct {
			importSystemSecret
			order int
		}
		var secrets []ordered
		for _, row := range shopWideSettingRows(rows) {
			if !isSecret(row.Key) {
				continue
			}
			val := unwrapSettingValue(row.ValueJSON)
			form := buildSettingForm(manifest, row.Key, "", nil, locale)
			secrets = append(secrets, ordered{importSystemSecret{Key: row.Key, Label: form.Label, Set: val != ""}, form.order})
		}
		sort.SliceStable(secrets, func(i, j int) bool { return secrets[i].order < secrets[j].order })
		for _, s := range secrets {
			v.Secrets = append(v.Secrets, s.importSystemSecret)
		}
		out = append(out, v)
	}
	return out, nil
}

// importSystemSample is one sample row as the result fragment shows it.
type importSystemSample struct {
	Row         int
	Name        string
	ExternalID  string
	Action      string
	ActionLabel string
	Changes     string // translated field names, comma-joined
	Issue       string // catsync's own wording
}

// importSystemView is what the result fragment renders.
type importSystemView struct {
	PluginID, EntryKey, Mode string
	Message                  string // the plugin's own message
	Counts                   catsync.Result
	Samples                  []importSystemSample
	Err                      *apiImportError
	// Filled by renderImportSystemResult.
	ErrText string
	Retry   bool
	Partial bool
}

// importSystemChangeKeys maps catsync's change names to locale keys.
var importSystemChangeKeys = map[string]string{
	"name": "import.system.change.name", "price": "import.system.change.price",
	"category": "import.system.change.category", "vat": "import.system.change.vat",
	"active": "import.system.change.active", "barcode": "import.system.change.barcode",
	"stock": "import.system.change.stock",
}

// importSystemActionKeys maps catsync's sample actions to locale keys.
var importSystemActionKeys = map[string]string{
	catsync.ActionNew: "import.system.count.new", catsync.ActionUpdated: "import.system.count.updated",
	catsync.ActionUnchanged: "import.system.count.unchanged", catsync.ActionIssue: "import.system.count.issues",
}

func registerImportFromSystem(mux *http.ServeMux, d *common.Deps) {
	// POST /api/import/from-system runs one preview or commit of an
	// installed API importer and answers an htmx fragment for the entry's
	// result area. Errors are rendered too (text/html, non-2xx), which
	// app.js's htmx:beforeSwap force-swaps into the same aria-live area.
	mux.HandleFunc("POST /api/import/from-system", func(w http.ResponseWriter, r *http.Request) {
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
		mode := strings.TrimSpace(r.PostForm.Get("mode"))
		v := importSystemView{PluginID: pluginID, EntryKey: entryKey, Mode: mode}

		entries, err := data.NewPluginRepo(d.Db).ListImportEntries(r.Context())
		if err != nil {
			logging.L().Errorf("[import-system] list import entries: %v", err)
			v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
			renderImportSystemResult(w, r, v)
			return
		}
		var entry data.ImportEntryRow
		found := false
		for _, e := range entries {
			if e.PluginID == pluginID && e.Key == entryKey && e.Source == plugins.ImportSourceAPI {
				entry, found = e, true
				break
			}
		}
		if !found {
			v.Err = &apiImportError{status: http.StatusNotFound, key: apiImportErrNotInstalled}
			renderImportSystemResult(w, r, v)
			return
		}
		granted, err := grantedImportEntities(r.Context(), d, entry, "")
		if err != nil {
			logging.L().Errorf("[import-system] %s: permissions: %v", pluginID, err)
			v.Err = &apiImportError{status: http.StatusInternalServerError, key: apiImportErrFailed}
			renderImportSystemResult(w, r, v)
			return
		}
		out, aerr := executeAPISourceImport(r, d, entry, granted, mode)
		if aerr != nil {
			v.Err = aerr
			renderImportSystemResult(w, r, v)
			return
		}
		v.Message, v.Counts = out.message, out.res
		renderImportSystemResult(w, r, v)
	})
}

// renderImportSystemResult renders the result fragment with the run's
// status code (2xx on success, the error's own status otherwise).
func renderImportSystemResult(w http.ResponseWriter, r *http.Request, v importSystemView) {
	locale := httpx.ResolveLocale(w, r)
	funcs := httpx.FuncsFor(locale)
	T := funcs["T"].(func(string) string)
	status := http.StatusOK
	if v.Err != nil {
		status = v.Err.status
		switch v.Err.key {
		case apiImportErrDeclined:
			if v.Err.detail != "" {
				v.ErrText = fmt.Sprintf(T(apiImportErrDeclined), v.Err.detail)
			} else {
				v.ErrText = T("import.system.error.declined_silent")
			}
		case apiImportErrPartial:
			v.ErrText = fmt.Sprintf(T(apiImportErrPartial), v.Err.applied)
			v.Partial = true
		default:
			v.ErrText = T(v.Err.key)
		}
		// A failed commit can safely run again (rows are linked) — except
		// where running again cannot help.
		v.Retry = v.Mode == "commit" && v.Err.key != apiImportErrReplica && v.Err.key != apiImportErrNoEntities && v.Err.key != apiImportErrNotInstalled
	}
	samples := v.Counts.Samples
	if len(samples) > catsync.MaxSamples {
		samples = samples[:catsync.MaxSamples]
	}
	for _, s := range samples {
		view := importSystemSample{Row: s.Row, Name: s.Name, ExternalID: s.ExternalID, Action: s.Action}
		if s.Issue != "" || s.IssueCode != "" {
			view.Issue = importSystemIssueText(T, s)
		}
		if k, ok := importSystemActionKeys[s.Action]; ok {
			view.ActionLabel = T(k)
		} else {
			view.ActionLabel = s.Action
		}
		var changes []string
		for _, c := range s.Changes {
			if k, ok := importSystemChangeKeys[c]; ok {
				changes = append(changes, T(k))
			} else {
				changes = append(changes, c)
			}
		}
		view.Changes = strings.Join(changes, ", ")
		v.Samples = append(v.Samples, view)
	}
	t, err := httpx.ClonedTemplate("pages.importSystemResult", "import_system_result.html", funcs, "ui/partials/import_system_result.html")
	var buf bytes.Buffer
	if err == nil {
		err = t.Execute(&buf, v)
	}
	if err != nil {
		logging.L().Errorf("[import-system] render result: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		httpx.RenderNotice(w, locale, "error", apiImportErrFailed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// importSystemIssueText renders a sample's row issue in the till's language
// from its code and args (ut-docs#4485), like translateImportIssue does for
// the file import. A code with no key never puts catsync's machine text on
// screen.
func importSystemIssueText(T func(string) string, s catsync.Sample) string {
	if s.IssueCode != "" {
		key := "import.issue." + s.IssueCode
		if text := T(key); text != key {
			args := make([]any, len(s.IssueArgs))
			for i, a := range s.IssueArgs {
				args[i] = a
			}
			return fmt.Sprintf(text, args...)
		}
	}
	logging.L().Warnf("[import-system] unrecognised row issue code %q", s.IssueCode)
	return T("import.status.unknown_issue")
}

// shopWideSettingRows keeps one row per key in first-seen order, the
// shop-wide (global) row when a key also has a per-till one — the same
// rule the plugin settings editor uses.
func shopWideSettingRows(rows []data.PluginSettingRow) []data.PluginSettingRow {
	idx := map[string]int{}
	var out []data.PluginSettingRow
	for _, row := range rows {
		if i, ok := idx[row.Key]; ok {
			if row.Scope == "global" {
				out[i] = row
			}
			continue
		}
		idx[row.Key] = len(out)
		out = append(out, row)
	}
	return out
}
