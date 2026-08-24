package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/taxrate"
)

// isSecretSettingKey reports whether a plugin setting holds a credential that
// should be masked (rendered as a password field, value never sent to the
// page). Heuristic on the key name — covers api keys, tokens, secrets,
// passwords, and the connector auth value.
func isSecretSettingKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"secret", "token", "password", "passwd", "api_key", "apikey", "auth_value", "private_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return strings.HasSuffix(k, "_key") || k == "key"
}

// isTaxRateOverridesKey reports whether a plugin setting is a per-tax-code
// takeaway rate override map (ut-docs#190) — a settings-surface convention
// any tax plugin can adopt (same family as isSecretSettingKey): the
// generic text editor is replaced with a typed, one-row-per-tax-code
// editor instead of raw JSON. ut-plugin-tax-de's takeaway_rate_overrides
// setting is the first (and so far only) adopter.
func isTaxRateOverridesKey(key string) bool {
	return key == "takeaway_rate_overrides"
}

// taxOverrideRow is one row of the typed takeaway-overrides editor: either
// an active tax code (Orphan=false) or an existing override entry whose
// tax_code_id no longer matches an active tax code (Orphan=true, kept only
// so it can be cleared).
type taxOverrideRow struct {
	TaxCodeID          string
	Name               string
	DineInPercent      string // display only
	OverridePercent    string // current value as a percent string, "" if unset
	PlaceholderPercent string // suggested value (pinned takeaway rate), "" if none
	Orphan             bool
}

// unwrapSettingValue unwraps a plugin_settings.value_json's JSON-string
// encoding to its plain text (same unwrap hostSettingsGet performs), so
// callers work with what the plugin itself would see.
func unwrapSettingValue(valueJSON string) string {
	var v string
	if json.Unmarshal([]byte(valueJSON), &v) != nil {
		return valueJSON // non-string JSON: pass through raw
	}
	return v
}

// buildTaxOverrideRows assembles the typed editor's rows from the current
// stored overrides map and the shop's active tax codes. Returns ok=false
// when there's nothing to show typed (no active tax codes and no existing
// overrides) — the caller falls back to the plain text input.
func buildTaxOverrideRows(currentValue string, taxCodes []data.TaxCodeView) ([]taxOverrideRow, bool) {
	overrides := map[string]int{}
	if trimmed := strings.TrimSpace(currentValue); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &overrides); err != nil {
			// A stored value that doesn't parse (hand-edited raw JSON?) must
			// never be silently masked as an empty editor and overwritten on
			// the next save — fall back to the raw text input so the actual
			// value stays visible and fixable (same stance as
			// MergeAdditiveJSONMapSetting's refuse-to-clobber rule).
			return nil, false
		}
	}
	if len(taxCodes) == 0 && len(overrides) == 0 {
		return nil, false
	}
	var rows []taxOverrideRow
	seen := map[string]bool{}
	for _, tc := range taxCodes {
		seen[tc.ID] = true
		row := taxOverrideRow{
			TaxCodeID:     tc.ID,
			Name:          tc.Name,
			DineInPercent: taxrate.FormatPercent(int(tc.RateBP)),
		}
		if bp, ok := overrides[tc.ID]; ok && bp > 0 {
			row.OverridePercent = taxrate.FormatPercent(bp)
		}
		if tc.TakeawayRateBP != nil {
			row.PlaceholderPercent = taxrate.FormatPercent(int(*tc.TakeawayRateBP))
		}
		rows = append(rows, row)
	}
	var orphanIDs []string
	for id := range overrides {
		if !seen[id] {
			orphanIDs = append(orphanIDs, id)
		}
	}
	sort.Strings(orphanIDs)
	for _, id := range orphanIDs {
		rows = append(rows, taxOverrideRow{
			TaxCodeID:       id,
			Name:            id,
			OverridePercent: taxrate.FormatPercent(overrides[id]),
			Orphan:          true,
		})
	}
	return rows, true
}

// httpStatusError carries a specific HTTP status + already-localized message
// out of parseTaxOverrides, so the caller can respond exactly rather than
// falling back to a generic 500.
type httpStatusError struct {
	status int
	msg    string
}

func (e httpStatusError) Error() string { return e.msg }

// parseTaxOverrides validates the typed takeaway-overrides submission and
// assembles the resulting tax_code_id -> basis-points map, starting FROM the
// currently stored map: an entry whose field is ABSENT from the form is
// preserved (the rendered form may predate that tax code or entry — replacing
// the whole map from the form alone silently deleted entries a concurrent
// writer, e.g. a catalog import's MergeAdditiveJSONMapSetting, had added), a
// present-but-blank field explicitly removes the entry, and a valid value
// sets it. Pure validation — no writes — so a caller can run it BEFORE any
// setting is persisted.
//
// Rate validation happens on the float, before the int conversion: NaN, ±Inf
// and out-of-range floats must be rejected here because int(math.Round(x))
// is implementation-defined for non-finite/overflowing x — on amd64 it
// yields math.MinInt64, which would be silently persisted into a live tax
// setting (review finding on ut-docs#190). A non-blank value that rounds to
// 0 bp is rejected too: the plugin ignores bp<=0, so "saving" it would
// silently store nothing while reporting success.
func parseTaxOverrides(form map[string][]string, current map[string]int, taxCodes []data.TaxCodeView, locale string) (map[string]int, error) {
	// Only tax_code_ids that are either an active tax code or an existing
	// orphan entry are accepted — the form cannot invent entries.
	allowed := map[string]bool{}
	for _, tc := range taxCodes {
		allowed[tc.ID] = true
	}
	overrides := map[string]int{}
	for id, bp := range current {
		allowed[id] = true
		overrides[id] = bp
	}
	invalid := httpStatusError{status: http.StatusBadRequest, msg: httpx.T(locale, "plugins.settings.takeaway.invalid_rate")}
	for id := range allowed {
		vals, ok := form["takeaway_pct_"+id]
		if !ok || len(vals) == 0 {
			continue // field absent from the form: preserve the stored entry
		}
		val := strings.TrimSpace(vals[0])
		if val == "" {
			delete(overrides, id) // present but blank: explicit removal
			continue
		}
		pct, err := strconv.ParseFloat(val, 64)
		if err != nil || math.IsNaN(pct) || math.IsInf(pct, 0) || pct < 0 || pct > 100 {
			return nil, invalid
		}
		bp := int(math.Round(pct * 100))
		if bp <= 0 {
			return nil, invalid
		}
		overrides[id] = bp
	}
	return overrides, nil
}

// writeTaxOverrides persists an already-validated overrides map using the
// exact same JSON-string encoding the generic settings path uses (so
// settings_get/hostSettingsGet's unwrap keeps working unchanged). Returns
// the number of settings changed (0 or 1).
func writeTaxOverrides(ctx context.Context, repo *data.PluginRepo, pluginID string, row data.PluginSettingRow, overrides map[string]int) (int, error) {
	marshaled, err := json.Marshal(overrides)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(string(marshaled))
	if err != nil {
		return 0, err
	}
	if string(raw) == row.ValueJSON {
		return 0, nil
	}
	if err := repo.UpsertPluginSettingScoped(ctx, pluginID, row.Key, string(raw), row.Scope); err != nil {
		return 0, err
	}
	return 1, nil
}

// Generic plugin settings editor (docs: architecture/ai-plugin.md): any
// installed plugin whose manifest declares settings gets an edit form.
// This is the surface the AI plugin's endpoint/model settings use, and
// what payment-terminal plugins will use for sandbox toggles etc.
func registerPluginSettings(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewPluginRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	catalogRepo := data.NewCatalogRepo(d.Db)

	type settingView struct {
		Key, Value string
		Secret     bool // rendered masked; its value is never sent to the page
		IsSet      bool // a value already exists (for the "leave blank to keep" hint)
		PerTill    bool // register-scoped: this till's own value, never synced
		Typed      bool // rendered as the typed takeaway-overrides editor instead
		TaxRows    []taxOverrideRow
	}

	mux.HandleFunc("GET /plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			http.Redirect(w, r, "/plugins", http.StatusSeeOther)
			return
		}
		pluginID := r.PathValue("id")
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
			return
		}
		var views []settingView
		for _, row := range rows {
			var v string
			if json.Unmarshal([]byte(row.ValueJSON), &v) != nil {
				v = row.ValueJSON // non-string JSON edits raw
			}
			sv := settingView{Key: row.Key, Value: v, Secret: isSecretSettingKey(row.Key), PerTill: row.Scope == "register"}
			if sv.Secret {
				sv.IsSet = v != ""
				sv.Value = "" // never render a secret's value into the page
			}
			if isTaxRateOverridesKey(row.Key) {
				taxCodes, err := catalogRepo.ListTaxCodes(r.Context())
				if err != nil {
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
					return
				}
				if taxRows, ok := buildTaxOverrideRows(v, taxCodes); ok {
					sv.Typed = true
					sv.TaxRows = taxRows
				}
			}
			views = append(views, sv)
		}
		httpx.Render("ui/pages/plugin_settings.html", map[string]any{
			"title":     "Plugin settings",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"PluginID":  pluginID,
			"Settings":  views,
		})(w, r)
	})

	mux.HandleFunc("POST /api/plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		pluginID := r.PathValue("id")
		_ = r.ParseForm()
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
			return
		}
		changed := 0
		locale := httpx.ResolveLocale(w, r)
		// Validate any typed takeaway-overrides submission BEFORE the write
		// loop: rows are written in key order, so a mid-loop validation abort
		// would leave earlier keys' upserts committed with no generation bump
		// and no audit row — a sibling setting half-saved while the plugin
		// keeps serving cached answers (ut-docs#190 review finding).
		typedOverrides := map[string]map[string]int{}
		if r.Form.Get("setting_takeaway_typed") == "1" {
			for _, row := range rows {
				if !isTaxRateOverridesKey(row.Key) {
					continue
				}
				taxCodes, err := catalogRepo.ListTaxCodes(r.Context())
				if err != nil {
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
					return
				}
				current := map[string]int{}
				_ = json.Unmarshal([]byte(unwrapSettingValue(row.ValueJSON)), &current)
				parsed, err := parseTaxOverrides(r.Form, current, taxCodes, locale)
				if err != nil {
					if statusErr, ok := err.(httpStatusError); ok {
						http.Error(w, statusErr.msg, statusErr.status)
						return
					}
					// Defensive: parseTaxOverrides' only non-nil error today is
					// the httpStatusError branch above — this exists so a future
					// change to that function fails safely (localized, not raw)
					// rather than silently regressing to a leak.
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
					return
				}
				typedOverrides[row.Key] = parsed
			}
		}
		for _, row := range rows {
			if parsed, ok := typedOverrides[row.Key]; ok {
				n, err := writeTaxOverrides(r.Context(), repo, pluginID, row, parsed)
				if err != nil {
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
					return
				}
				changed += n
				continue
			}
			// Only keys the plugin declared are writable — the form cannot
			// invent settings.
			form, ok := r.Form["setting_"+row.Key]
			if !ok || len(form) == 0 {
				continue
			}
			val := strings.TrimSpace(form[0])
			// Secret fields aren't pre-filled, so a blank submission means
			// "keep the current value" rather than "clear it".
			if val == "" && isSecretSettingKey(row.Key) {
				continue
			}
			raw, err := json.Marshal(val)
			if err != nil {
				continue
			}
			if string(raw) == row.ValueJSON {
				continue
			}
			// Write back into the row's own scope: a register-scoped setting
			// (per-till, e.g. a card reader id) must not become shop-wide.
			if err := repo.UpsertPluginSettingScoped(r.Context(), pluginID, row.Key, string(raw), row.Scope); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "pluginsettings.error.server", "plugin_settings", err)
				return
			}
			changed++
		}
		if changed > 0 {
			// A setting can feed a plugin's ".ask" answers (settings_get host
			// fn) — drop cached answers or the till keeps charging the old
			// rate until an unrelated reload (ut-docs#222 review finding).
			plugins.SharedBus(d.Db).BumpGeneration()
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "plugin", pluginID, "plugin_settings_saved",
			map[string]any{"changed": changed}, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s (%d)</span>`, httpx.T(locale, "plugins.settings.saved"), changed)
	})
}
