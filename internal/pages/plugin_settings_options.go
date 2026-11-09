package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Asked options (ADR-0121 S5, ut-docs#4160): a `choice` with
// `options_ask: true` takes its dropdown entries from the owning plugin
// through ui.settings.options.ask. The page never waits for the plugin: the
// GET draws the stored value and the static options, and an htmx fragment
// per asked choice brings the plugin's list in.

const settingsOptionsAskEvent = "ui.settings.options.ask"

// settingsOptionsTimeout is a var so tests can shorten it.
var settingsOptionsTimeout = 5 * time.Second

const (
	settingsOptionsMaxAnswerBytes = 16 << 10
	settingsOptionsMaxFormBytes   = 64 << 10
)

// settingsOptionsAsk is the payload. #4158 adds a record_id.
type settingsOptionsAsk struct {
	Key    string            `json:"key"`
	Values map[string]string `json:"values"`
}

type askedOption struct{ Value, Label string }

// decodeSettingsOptionsAnswer is the strict decoder of the plugin's answer:
// any violation rejects the whole answer, nothing is applied partially.
func decodeSettingsOptionsAnswer(raw []byte) ([]askedOption, error) {
	if len(raw) > settingsOptionsMaxAnswerBytes {
		return nil, fmt.Errorf("answer is %d bytes, over %d", len(raw), settingsOptionsMaxAnswerBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var ans *struct {
		Options []json.RawMessage `json:"options"`
	}
	if err := dec.Decode(&ans); err != nil {
		return nil, fmt.Errorf("answer is not a JSON object: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the answer")
	}
	if ans == nil || len(ans.Options) == 0 { // null, {} and [] are all "no options"
		return nil, nil
	}
	if len(ans.Options) > plugins.MaxAskedOptions {
		return nil, fmt.Errorf("%d options, over %d", len(ans.Options), plugins.MaxAskedOptions)
	}
	seen := make(map[string]bool, len(ans.Options))
	out := make([]askedOption, 0, len(ans.Options))
	for i, rawOpt := range ans.Options {
		var o struct {
			Value string `json:"value"`
			Label string `json:"label"`
		}
		od := json.NewDecoder(bytes.NewReader(rawOpt))
		od.DisallowUnknownFields()
		if err := od.Decode(&o); err != nil {
			return nil, fmt.Errorf("option %d: %w", i, err)
		}
		if !plugins.AskedChoiceValueOK(o.Value) {
			return nil, fmt.Errorf("option %d: value is not allowed", i)
		}
		if seen[o.Value] {
			return nil, fmt.Errorf("option %d: duplicate value", i)
		}
		seen[o.Value] = true
		if len(o.Label) > plugins.MaxAskedOptionLabelBytes || !utf8.ValidString(o.Label) || strings.ContainsFunc(o.Label, unicode.IsControl) {
			return nil, fmt.Errorf("option %d: label is not allowed", i)
		}
		if o.Label == "" {
			o.Label = o.Value
		}
		out = append(out, askedOption{Value: o.Value, Label: o.Label})
	}
	return out, nil
}

// settingsAskValues is the form's current non-secret top-level values
// (unsaved ones included) as settings_get would return them once saved: the
// submitted value when the form carries it, else the stored one. Only
// manifest-declared keys; never a secret, a list or a map.
func settingsAskValues(m *plugins.Manifest, form url.Values, rows []data.PluginSettingRow, isSecret func(string) bool, decimals int, locale string) map[string]string {
	out := map[string]string{}
	if m == nil {
		return out
	}
	stored := storedControlValues(rows)
	for i := range m.Settings {
		decl := &m.Settings[i]
		key := decl.Key
		if isSecret(key) || decl.Type == plugins.SettingTypeSecret || decl.Type == plugins.SettingTypeList || decl.Type == plugins.SettingTypeMap {
			continue
		}
		sub, submitted := form["setting_"+key]
		switch kind := settingFieldKind(decl); {
		case kind == plugins.SettingTypeBool:
			if on, ok := submittedBool(form, key); ok {
				out[key] = fmt.Sprint(on)
				continue
			}
		case kind != "":
			if submitted && len(sub) > 0 {
				vj, fe := validateNumberField(decl, sub[0], decimals, locale)
				if fe == nil && vj != "null" {
					out[key] = vj
				}
				continue // an invalid or blank number is omitted, not replaced by the stored one
			}
		default:
			if submitted && len(sub) > 0 {
				out[key] = strings.TrimSpace(sub[0])
				continue
			}
		}
		if v, ok := stored[key]; ok {
			out[key] = v
		}
	}
	return out
}

// askSettingsOptions answers from the cache or asks the plugin. failed is
// true on a timeout, error, panic or malformed answer; no answer at all
// (nobody listening, declined) is an empty list and is not cached.
func askSettingsOptions(ctx context.Context, d *common.Deps, pluginID, version, key string, values map[string]string) (opts []askedOption, failed bool) {
	ck := settingsOptionsCacheKey(version, key, values)
	if cached, ok := sharedSettingsOptionsCache.get(pluginID, ck); ok {
		return cached, false
	}
	raw, answered, err := boundedAskPlugin(ctx, d, pluginID, settingsOptionsAskEvent, settingsOptionsAsk{Key: key, Values: values}, settingsOptionsTimeout)
	if err != nil {
		logging.L().Infof("plugin settings: options ask to %s for %q failed: %v", pluginID, key, err) // never the values
		return nil, true
	}
	if !answered {
		return nil, false
	}
	opts, err = decodeSettingsOptionsAnswer(raw)
	if err != nil {
		logging.L().Infof("plugin settings: options answer from %s for %q rejected: %v", pluginID, key, err)
		return nil, true
	}
	sharedSettingsOptionsCache.put(pluginID, ck, opts)
	return opts, false
}

// mergeChoiceOptions is a choice's dropdown entries: the "not set" entry
// when current is empty; a current value found in neither list kept first
// and selected (a plain legacy value is flagged Unknown, an asked one is
// legitimate); then the plugin's options in answer order; then the static
// options not already present.
func mergeChoiceOptions(decl *plugins.ManifestSetting, current string, asked []askedOption, locale string) []choiceOptionView {
	var out []choiceOptionView
	inAsked := false
	for _, a := range asked {
		if a.Value == current {
			inAsked = true
		}
	}
	switch {
	case current == "":
		// Unset: say so, rather than letting the browser preselect
		// option 1 and a sibling's save silently write it.
		out = append(out, choiceOptionView{Value: "", Label: httpx.T(locale, "plugins.settings.secret_empty"), Selected: true})
	case !inAsked && !decl.ChoiceAllows(current):
		// A value outside the options stays selectable as itself.
		out = append(out, choiceOptionView{Value: current, Label: current, Selected: true, Unknown: !decl.OptionsAsk, Asked: decl.OptionsAsk})
	}
	present := map[string]bool{}
	for _, a := range asked {
		present[a.Value] = true
		out = append(out, choiceOptionView{Value: a.Value, Label: a.Label, Selected: a.Value == current, Asked: true})
	}
	for _, o := range decl.Options {
		if present[o.Value] {
			continue
		}
		out = append(out, choiceOptionView{Value: o.Value, Label: httpx.T(locale, o.Label), Selected: o.Value == current})
	}
	return out
}

// askedOptionsMessage picks the one line shown under an asked dropdown:
// failed > saved_missing > empty ("" = none).
func askedOptionsMessage(decl *plugins.ManifestSetting, current string, asked []askedOption, failed bool) string {
	if failed {
		return "failed"
	}
	if len(asked) == 0 {
		if len(decl.Options) == 0 {
			return "empty"
		}
		return ""
	}
	if current == "" || decl.ChoiceAllows(current) {
		return ""
	}
	for _, a := range asked {
		if a.Value == current {
			return ""
		}
	}
	return "saved_missing"
}

// askOnKeys is the settings whose change re-asks a choice: its depends_on
// plus its show_when controller, deduplicated, comma-joined.
func askOnKeys(decl *plugins.ManifestSetting) string {
	var keys []string
	add := func(k string) {
		for _, e := range keys {
			if e == k {
				return
			}
		}
		keys = append(keys, k)
	}
	for _, k := range decl.DependsOn {
		add(k)
	}
	if decl.ShowWhen != nil {
		add(decl.ShowWhen.Key)
	}
	return strings.Join(keys, ",")
}

const settingsOptionsBlock = "plugin_settings_ask_fragment"

// registerSettingsOptions registers POST /api/plugins/{id}/settings/{key}/options:
// the htmx fragment of one asked choice. It is a POST so the form's current
// values travel in the body, not in a URL or a log.
func registerSettingsOptions(mux *http.ServeMux, d *common.Deps, repo *data.PluginRepo) {
	mux.HandleFunc("POST /api/plugins/{id}/settings/{key}/options", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// The ask makes the plugin run (and reach out): refuse a cross-site POST.
		if !sameOriginRequest(r) {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		pluginID, key := r.PathValue("id"), r.PathValue("key")
		version, installed, err := repo.GetInstalledPluginVersion(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_options", err)
			return
		}
		if !installed {
			common.LocalizedError(w, r, http.StatusNotFound, "plugins.install.error.not_found")
			return
		}
		m, _, err := plugins.InstalledManifest(r.Context(), d.Db, pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_options", err)
			return
		}
		decl := m.SettingDecl(key) // nil-safe
		if decl == nil || decl.Type != plugins.SettingTypeChoice || !decl.OptionsAsk {
			common.LocalizedError(w, r, http.StatusNotFound, "plugins.install.error.not_found")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, settingsOptionsMaxFormBytes)
		if err := r.ParseForm(); err != nil {
			common.LocalizedError(w, r, http.StatusRequestEntityTooLarge, "plugins.error.server")
			return
		}
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_options", err)
			return
		}
		var row *data.PluginSettingRow
		scope := r.URL.Query().Get("scope")
		for i := range rows {
			if rows[i].Key != key {
				continue
			}
			if row == nil || rows[i].Scope == scope {
				row = &rows[i]
			}
		}
		if row == nil { // the form draws stored rows only
			common.LocalizedError(w, r, http.StatusNotFound, "plugins.install.error.not_found")
			return
		}
		isSecret, _, _, resolved := secretSettingCheck(r.Context(), d, pluginID)
		if !resolved {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_options",
				fmt.Errorf("plugin %s: manifest could not be resolved", pluginID))
			return
		}
		locale := httpx.ResolveLocale(w, r)
		values := settingsAskValues(m, r.Form, rows, isSecret, httpx.ActiveCurrency().Decimals, locale)
		opts, failed := askSettingsOptions(r.Context(), d, pluginID, version, key, values)

		stored := unwrapSettingValue(row.ValueJSON)
		current := stored
		if vals, ok := r.Form["setting_"+key]; ok && len(vals) > 0 {
			if sub := strings.TrimSpace(vals[0]); plugins.AskedChoiceValueOK(sub) || sub == strings.TrimSpace(stored) {
				current = sub
			}
		}
		view := pluginSettingView{
			Key: key, Value: current, PerTill: row.Scope == "register", PluginID: pluginID,
			Notice: settingNoticeKey(pluginID, key, current), Required: decl.Required,
		}
		view.Label = key
		if decl.Label != "" {
			view.Label = httpx.T(locale, decl.Label)
		}
		if decl.Help != "" {
			view.Help = httpx.T(locale, decl.Help)
		}
		view.Choice, view.Ask, view.AskOn = true, true, askOnKeys(decl)
		view.Options = mergeChoiceOptions(decl, current, opts, locale)
		view.AskFailed = failed
		view.AskMsg = askedOptionsMessage(decl, current, opts, failed)
		view.Invalid = r.Form.Get("ps_ask_invalid") == "1"
		httpx.RenderBlock("ui/pages/plugin_settings.html", settingsOptionsBlock, view)(w, r)
	})
}
