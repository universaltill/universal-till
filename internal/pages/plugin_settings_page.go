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
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/taxrate"
)

// secretSettingCheck returns the "is this key a credential?" predicates for
// one plugin's settings page (ADR-0082, ut-docs#1739): isSecret is masked
// (password field, value never sent to the page) when either the key-name
// heuristic (plugins.IsSecretSettingKey — the same rule internal/data seals
// on) matches OR the plugin's manifest declares the key `type: "secret"`;
// declaredSecret reads the manifest half directly (not derived from isSecret
// by subtraction — a prior version of this function computed it as
// `isSecret(key) && !plugins.IsSecretSettingKey(key)`, which only stayed
// correct because isSecret was exactly heuristic-or-declared; a derived copy
// of a rule this codebase's own conventions warn against, review finding).
//
// resolved is false only when the plugin's manifest could NOT be
// authoritatively determined (a DB error, a path-traversal guard trip, or an
// active plugin version whose manifest.json is missing/corrupt —
// InstalledManifest's own doc comment draws this line) — as opposed to a
// plugin that simply has no active version, which is a legitimate "declares
// nothing" state (resolved=true, manifest=nil). GET (display/masking) may
// safely degrade to the heuristic alone when resolved is false — that is
// display-only, never an at-rest exposure. The POST handler MUST refuse to
// write when resolved is false instead of degrading: an unresolved manifest
// could be hiding a `type: "secret"` declaration on a key the heuristic
// doesn't catch, and writing that key's value while unable to prove it
// isn't declared secret would risk sealing nothing when it should have
// sealed — this was ut-docs#1739's review blocker (a manifest read failure
// silently stored a live credential in cleartext with a 200 OK).
func secretSettingCheck(ctx context.Context, d *common.Deps, pluginID string) (isSecret func(key string) bool, declaredSecret func(key string) bool, resolved bool) {
	manifest, _, err := plugins.InstalledManifest(ctx, d.Db, pluginID)
	resolved = err == nil
	if err != nil {
		logging.L().Warnf("plugin settings: could not read manifest for %s (%v) — display falls back to the key-name heuristic; writes are refused until this resolves (ADR-0082)", pluginID, err)
		manifest = nil
	}
	isSecret = func(key string) bool {
		return plugins.IsSecretSettingKey(key) || manifest.SettingDeclaredSecret(key)
	}
	declaredSecret = func(key string) bool {
		return manifest.SettingDeclaredSecret(key)
	}
	return isSecret, declaredSecret, resolved
}

// isTaxRateOverridesKey reports whether a plugin setting is a per-tax-code
// takeaway rate override map (ut-docs#190) — a settings-surface convention
// any tax plugin can adopt (same family as plugins.IsSecretSettingKey): the
// generic text editor is replaced with a typed, one-row-per-tax-code
// editor instead of raw JSON. ut-plugin-tax-de's takeaway_rate_overrides
// setting is the first (and so far only) adopter.
func isTaxRateOverridesKey(key string) bool {
	return key == "takeaway_rate_overrides"
}

// settingNoticeKey returns the i18n key of a disclosure rendered ABOVE one
// specific plugin setting's input, or "" for every other field. Today the
// only adopter is the AI plugin's hosted-provider api_key (ADR-0085,
// ut-docs#1708): an operator must read what a hosted vendor receives before
// they can type a key, so the notice is scoped to that plugin's that key —
// not to every plugin's every secret field (same per-plugin-key
// special-case family as isTaxRateOverridesKey). The template only renders
// this inside the `.Secret` branch (review finding, ut-docs#1708) — every
// key this returns non-"" for today also matches the secret heuristic, so
// that's never been observed, but a future adopter on a plain-text setting
// would get a silently-dropped notice; wire a non-secret render path first.
func settingNoticeKey(pluginID, key string) string {
	if pluginID == AIPluginID && key == "api_key" {
		return "plugins.settings.ai.hosted_provider_notice"
	}
	return ""
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
// encoding to its plain text, so callers work with what the plugin itself
// would see. Delegates to data.DecodeMapSettingValue (ut-docs#1269), the
// shared decode seam also used by MergeAdditiveJSONMapSetting's read side.
// NOT the same unwrap hostSettingsGet performs (internal/plugins/
// wasm_hostfns.go) — that one unwraps unconditionally, so a stored bare
// `null` yields "" there vs. "null" here; deliberately left as its own
// independent seam by this card (ut-docs#1269 non-goal), harmless today
// because the one caller here (line ~360) only ranges over the result.
func unwrapSettingValue(valueJSON string) string {
	return data.DecodeMapSettingValue(valueJSON)
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
// settings_get/hostSettingsGet's unwrap keeps working unchanged) — via
// data.EncodeMapSettingValue (ut-docs#1269), the shared encode seam also
// used by MergeAdditiveJSONMapSetting's write side. Returns the number of
// settings changed (0 or 1).
func writeTaxOverrides(ctx context.Context, repo *data.PluginRepo, pluginID string, row data.PluginSettingRow, overrides map[string]int) (int, error) {
	raw, err := data.EncodeMapSettingValue(overrides)
	if err != nil {
		return 0, err
	}
	if raw == row.ValueJSON {
		return 0, nil
	}
	// A rate-override map is never a manifest-declared secret (declaredSecret
	// false); the repository's key-name heuristic still applies regardless.
	if err := repo.UpsertPluginSettingScoped(ctx, pluginID, row.Key, raw, row.Scope, false); err != nil {
		return 0, err
	}
	// Diagnostic-mode provenance (ADR-0092 §2, ut-docs#2169): a manager's
	// edit adds or removes active overrides per tax code. Tax code ids
	// only — the rate value and the code's name never travel.
	if diagnostics.Active() {
		before := map[string]int{}
		_ = json.Unmarshal([]byte(data.DecodeMapSettingValue(row.ValueJSON)), &before)
		for id := range overrides {
			if _, had := before[id]; !had {
				diagnostics.Emit(diagnostics.TaxProvenance{TaxCodeID: id, From: diagnostics.ProvenanceAbsent, To: diagnostics.ProvenanceActiveOverride})
			}
		}
		for id := range before {
			if _, still := overrides[id]; !still {
				diagnostics.Emit(diagnostics.TaxProvenance{TaxCodeID: id, From: diagnostics.ProvenanceActiveOverride, To: diagnostics.ProvenanceAbsent})
			}
		}
	}
	return 1, nil
}

// Generic plugin settings editor (docs: architecture/ai-plugin.md): any
// installed plugin whose manifest declares settings gets an edit form.
// This is the surface the AI plugin's endpoint/model settings use, and
// what payment-terminal plugins will use for sandbox toggles etc.
func registerPluginSettings(mux *http.ServeMux, d *common.Deps) {
	// A setting can feed a plugin's ".ask" answers (settings_get host fn) —
	// cached answers must be dropped or the till keeps charging the old rate
	// until an unrelated reload (ut-docs#222). The bump is wired into the
	// repo itself (ut-docs#1941) so every write through it — the generic
	// loop below AND writeTaxOverrides, which shares this instance — fires
	// it structurally, instead of each call site remembering to.
	repo := data.NewPluginRepo(d.Db).OnSettingsChanged(func() { plugins.SharedBus(d.Db).BumpGeneration() })
	posRepo := data.NewPOSRepo(d.Db)
	catalogRepo := data.NewCatalogRepo(d.Db)

	type settingView struct {
		Key, Value string
		Secret     bool // rendered masked; its value is never sent to the page
		IsSet      bool // a value already exists (for the "leave blank to keep" hint)
		PerTill    bool // register-scoped: this till's own value, never synced
		Typed      bool // rendered as the typed takeaway-overrides editor instead
		TaxRows    []taxOverrideRow
		Notice     string // i18n key of a disclosure shown above the input (settingNoticeKey); "" = none
	}

	mux.HandleFunc("GET /plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			http.Redirect(w, r, "/plugins", http.StatusSeeOther)
			return
		}
		pluginID := r.PathValue("id")
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "plugins.error.server", err)
			return
		}
		// Display only — safe to degrade to the heuristic if the manifest
		// can't be resolved right now, per secretSettingCheck's doc comment.
		isSecret, _, _ := secretSettingCheck(r.Context(), d, pluginID)
		var views []settingView
		for _, row := range rows {
			v := unwrapSettingValue(row.ValueJSON)
			sv := settingView{Key: row.Key, Value: v, Secret: isSecret(row.Key), PerTill: row.Scope == "register", Notice: settingNoticeKey(pluginID, row.Key)}
			if sv.Secret {
				sv.IsSet = v != ""
				sv.Value = "" // never render a secret's value into the page
			}
			if isTaxRateOverridesKey(row.Key) {
				taxCodes, err := catalogRepo.ListTaxCodes(r.Context())
				if err != nil {
					httpx.RenderError(w, r, http.StatusInternalServerError, "plugins.error.server", err)
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
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
			return
		}
		isSecret, declaredSecret, resolved := secretSettingCheck(r.Context(), d, pluginID)
		if !resolved {
			// Fail closed (ADR-0082, ut-docs#1739 review blocker): an
			// unresolved manifest could be hiding a `type: "secret"`
			// declaration on a key the heuristic doesn't catch. Refuse the
			// whole save rather than risk writing a credential in plaintext
			// — secretSettingCheck already logged the underlying cause.
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings",
				fmt.Errorf("plugin %s: manifest could not be resolved, refusing to save settings (ADR-0082)", pluginID))
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
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
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
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
					return
				}
				typedOverrides[row.Key] = parsed
			}
		}
		for _, row := range rows {
			if parsed, ok := typedOverrides[row.Key]; ok {
				n, err := writeTaxOverrides(r.Context(), repo, pluginID, row, parsed)
				if err != nil {
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
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
			if val == "" && isSecret(row.Key) {
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
			// A secret (declared or heuristic) is sealed at rest by the
			// repository (ADR-0082).
			if err := repo.UpsertPluginSettingScoped(r.Context(), pluginID, row.Key, string(raw), row.Scope, declaredSecret(row.Key)); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
				return
			}
			changed++
		}
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "plugin", pluginID, "plugin_settings_saved",
			map[string]any{"changed": changed}, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s (%d)</span>`, httpx.T(locale, "plugins.settings.saved"), changed)
	})
}
