package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/bgremove"
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
//
// declaredEndpoint reads the manifest's `type: "endpoint"` declarations
// (ADR-0121 §2, ut-docs#3328) off the same manifest read, so the POST
// handler can validate those values before it writes anything.
func secretSettingCheck(ctx context.Context, d *common.Deps, pluginID string) (isSecret, declaredSecret, declaredEndpoint func(key string) bool, resolved bool) {
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
	declaredEndpoint = func(key string) bool {
		return manifest.SettingDeclaredEndpoint(key)
	}
	return isSecret, declaredSecret, declaredEndpoint, resolved
}

// choiceOptionView is one entry of a `type: "choice"` setting's dropdown.
type choiceOptionView struct {
	Value, Label string
	Selected     bool
	Unknown      bool // a stored value outside the options (show_when then hides nothing)
}

// settingsFormManifest returns the plugin's installed manifest for its
// settings-form metadata (ut-docs#2832), or nil when it can't be read. Nil
// is safe on both sides: the page degrades to plain text boxes in storage
// order, and the POST handler has already refused to save when the manifest
// is unresolvable (secretSettingCheck), so a nil here only means the plugin
// declares nothing.
func settingsFormManifest(ctx context.Context, d *common.Deps, pluginID string) *plugins.Manifest {
	m, _, err := plugins.InstalledManifest(ctx, d.Db, pluginID)
	if err != nil {
		return nil
	}
	return m
}

// settingForm is one setting's settings-form metadata (ut-docs#2832).
type settingForm struct {
	Label   string             // translated label; the raw key when none is declared
	Help    string             // translated help line; "" = none
	Choice  bool               // `type: "choice"`: rendered as a dropdown of Options
	Options []choiceOptionView // dropdown entries, Selected = the stored value
	ShowKey string             // show_when.key; "" = always shown
	ShowIn  string             // show_when.in, comma-joined (values carry no comma, validated)
	Hidden  bool               // show_when not met by the stored values
	order   int                // manifest declaration index; undeclared keys sort last
}

// undeclaredSettingOrder sorts settings the manifest doesn't declare after
// every declared one (the stable sort keeps their storage order).
const undeclaredSettingOrder = 1 << 30

// buildSettingForm derives one setting's form metadata from its manifest
// declaration: translated label/help, dropdown options for a choice (a
// stored value that is not an option stays selectable as itself), the
// show_when condition evaluated against the stored values, and the
// declaration index the form is ordered by. A nil manifest or undeclared
// key gives the plain text box, labelled with its key.
func buildSettingForm(m *plugins.Manifest, key, value string, stored map[string]string, locale string) settingForm {
	f := settingForm{Label: key, order: undeclaredSettingOrder}
	decl := m.SettingDecl(key)
	if decl == nil {
		return f
	}
	for i := range m.Settings {
		if m.Settings[i].Key == key {
			f.order = i
			break
		}
	}
	if decl.Label != "" {
		f.Label = httpx.T(locale, decl.Label)
	}
	if decl.Help != "" {
		f.Help = httpx.T(locale, decl.Help)
	}
	if decl.Type == plugins.SettingTypeChoice {
		f.Choice = true
		switch {
		case value == "":
			// Unset: say so, rather than letting the browser preselect
			// option 1 and a sibling's save silently write it.
			f.Options = append(f.Options, choiceOptionView{Value: "", Label: httpx.T(locale, "plugins.settings.secret_empty"), Selected: true})
		case !decl.ChoiceAllows(value):
			// An older free-text value stays selectable as itself.
			f.Options = append(f.Options, choiceOptionView{Value: value, Label: value, Selected: true, Unknown: true})
		}
		for _, o := range decl.Options {
			f.Options = append(f.Options, choiceOptionView{Value: o.Value, Label: httpx.T(locale, o.Label), Selected: o.Value == value})
		}
	}
	if w := decl.ShowWhen; w != nil {
		f.ShowKey = w.Key
		f.ShowIn = strings.Join(w.In, ",")
		cur := stored[w.Key]
		// A controlling choice holding a value outside its options (an
		// older free-text entry) hides nothing: the plugin may still use
		// the dependent fields, so the operator must see them.
		ctl := m.SettingDecl(w.Key)
		f.Hidden = !(ctl != nil && ctl.Type == plugins.SettingTypeChoice && cur != "" && !ctl.ChoiceAllows(cur))
		for _, v := range w.In {
			if cur == v {
				f.Hidden = false
			}
		}
	}
	return f
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
// special-case family as isTaxRateOverridesKey). The template renders it
// above the input in both the `.Secret` branch and the plain-text branch.
//
// Second adopter (ut-docs#3126): the AI plugin's plain-text image_model,
// when its stored value is set but off the licence-checked allow-list
// (bgremove.ImageModelAllowed — rembg's bria-rmbg default among them). Resolution
// turns background removal off for such a value; the generic editor has no
// per-plugin save-time validation, so this hint says why (never a modal).
// value is the stored plain value; the resolver trims it the same way.
//
// Third adopter (ut-docs#4095): the AI plugin's plain-text image_endpoint,
// when its stored value is set but not a plain http(s) URL
// (plugins.ValidEndpointURL, the rule bgremove applies at resolve time). The
// setting is deliberately not `type: "endpoint"` (that would grant the WASM
// plugin http:lan egress to the rembg host), so save never checks it; such a
// value leaves the shop's own service unused and the till on its built-in
// background removal, and this hint says why.
func settingNoticeKey(pluginID, key, value string) string {
	if pluginID != AIPluginID {
		return ""
	}
	switch key {
	case "api_key":
		return "plugins.settings.ai.hosted_provider_notice"
	case "image_model":
		if v := strings.TrimSpace(value); v != "" && !bgremove.ImageModelAllowed(v) {
			return "plugins.settings.ai.image_model_unsupported"
		}
	case "image_endpoint":
		if v := strings.TrimSpace(value); v != "" && !plugins.ValidEndpointURL(v) {
			return "plugins.settings.ai.image_endpoint_invalid"
		}
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
	// InvalidStoredPercent is the stored entry's percent text when it is out
	// of the accepted range (bp <= 0 or > taxrate.MaxBP), "" otherwise. Such a
	// row renders a blank input (a bad prefill would block the whole form) and
	// a visible notice naming the stored value (ut-docs#4086).
	InvalidStoredPercent string
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
//
// Every percent string is localized with httpx.LocalizeMajor so a comma-locale
// till prefills "7,5" (ParsePercentBP accepts either separator on save).
func buildTaxOverrideRows(currentValue string, taxCodes []data.TaxCodeView, locale string) ([]taxOverrideRow, bool) {
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
	pct := func(bp int) string { return httpx.LocalizeMajor(taxrate.FormatPercent(bp), locale) }
	valid := func(bp int) bool { return bp > 0 && bp <= taxrate.MaxBP }
	var rows []taxOverrideRow
	seen := map[string]bool{}
	for _, tc := range taxCodes {
		seen[tc.ID] = true
		row := taxOverrideRow{
			TaxCodeID:     tc.ID,
			Name:          tc.Name,
			DineInPercent: pct(int(tc.RateBP)),
		}
		if bp, ok := overrides[tc.ID]; ok {
			if valid(bp) {
				row.OverridePercent = pct(bp)
			} else {
				row.InvalidStoredPercent = pct(bp)
			}
		}
		if tc.TakeawayRateBP != nil {
			row.PlaceholderPercent = pct(int(*tc.TakeawayRateBP))
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
		row := taxOverrideRow{TaxCodeID: id, Name: id, Orphan: true}
		if bp := overrides[id]; valid(bp) {
			row.OverridePercent = pct(bp)
		} else {
			row.InvalidStoredPercent = pct(bp)
		}
		rows = append(rows, row)
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
// The value is read by httpx.ParsePercentBP (ut-docs#2974): '.' or ',' as
// the separator, at most 2 decimals, digits only, so exponents, NaN, Inf and
// signs are refused by construction. A non-blank value of 0 bp is rejected
// too: the plugin ignores bp<=0, so "saving" it would silently store nothing
// while reporting success. Above 100% (10000 bp) is refused as well.
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
		bp, err := httpx.ParsePercentBP(val)
		if err != nil || bp <= 0 || bp > 10000 {
			return nil, invalid
		}
		overrides[id] = int(bp)
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
		Key, Value  string
		Secret      bool // rendered masked; its value is never sent to the page
		IsSet       bool // a value already exists (for the "leave blank to keep" hint)
		PerTill     bool // register-scoped: this till's own value, never synced
		Typed       bool // rendered as the typed takeaway-overrides editor instead
		TaxRows     []taxOverrideRow
		Notice      string // i18n key of a disclosure shown above the input (settingNoticeKey); "" = none
		settingForm        // settings-form metadata from the installed manifest (ut-docs#2832)
	}

	// permissionView is the read half of the permission-management surface
	// (ut-docs#2240): the write half (POST /api/plugins/permissions/grant
	// and /revoke, internal/pages/plugin_api.go) already existed and is
	// already gated + audited; this page just gives an operator somewhere
	// to see and act on it, alongside a plugin's other settings.
	type permissionView struct {
		permissionBadge
		Granted bool
	}

	mux.HandleFunc("GET /plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			http.Redirect(w, r, "/plugins", http.StatusSeeOther)
			return
		}
		pluginID := r.PathValue("id")
		// A plugin that isn't installed (a mistyped or shortened id) has no
		// settings page — rendering one said "no settings, no permissions",
		// which reads as a broken install (ut-docs#4135). A disabled plugin
		// is still installed and keeps its page.
		if _, installed, err := repo.GetInstalledPluginVersion(r.Context(), pluginID); err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "plugins.error.server", err)
			return
		} else if !installed {
			http.Redirect(w, r, "/plugins", http.StatusSeeOther)
			return
		}
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "plugins.error.server", err)
			return
		}
		// Display only — safe to degrade to the heuristic if the manifest
		// can't be resolved right now, per secretSettingCheck's doc comment.
		isSecret, _, _, _ := secretSettingCheck(r.Context(), d, pluginID)
		manifest := settingsFormManifest(r.Context(), d, pluginID)
		locale := httpx.RequestLocale(r)
		stored := make(map[string]string, len(rows))
		for _, row := range rows {
			// A key can have a global and a per-till row; show_when reads
			// the shop-wide one when both exist.
			if _, seen := stored[row.Key]; !seen || row.Scope == "global" {
				stored[row.Key] = unwrapSettingValue(row.ValueJSON)
			}
		}
		var views []settingView
		for _, row := range rows {
			v := unwrapSettingValue(row.ValueJSON)
			sv := settingView{Key: row.Key, Value: v, Secret: isSecret(row.Key), PerTill: row.Scope == "register", Notice: settingNoticeKey(pluginID, row.Key, v)}
			sv.settingForm = buildSettingForm(manifest, row.Key, v, stored, locale)
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
				if taxRows, ok := buildTaxOverrideRows(v, taxCodes, httpx.RequestLocale(r)); ok {
					sv.Typed = true
					sv.TaxRows = taxRows
				}
			}
			views = append(views, sv)
		}
		// The manifest's declaration order is the form's order (a provider
		// choice before the fields it controls); storage order is by key.
		sort.SliceStable(views, func(i, j int) bool { return views[i].order < views[j].order })
		// Display only, same standing as the settings list above — a manifest
		// read failure here degrades to an empty permissions section rather
		// than failing the whole page; the write side (plugin_api.go's
		// grant/revoke handlers) does its own gating independently.
		perms, err := plugins.ListPluginPermissions(r.Context(), d.Db, pluginID)
		if err != nil {
			logging.L().Warnf("plugin settings: could not list permissions for %s (%v) — rendering with none", pluginID, err)
			perms = nil
		}
		permViews := make([]permissionView, 0, len(perms))
		for _, p := range perms {
			b := describePermission(p.Name)
			if b.SettingKeys != "" {
				// Show what granting unlocks — a manifest default included.
				b.Target, _ = plugins.SettingBoundTarget(r.Context(), d.Db, pluginID, p.Name)
			}
			permViews = append(permViews, permissionView{permissionBadge: b, Granted: p.Granted})
		}
		httpx.Render("ui/pages/plugin_settings.html", map[string]any{
			"title":       httpx.T(httpx.RequestLocale(r), "page.title.plugin_settings"),
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.MenuSnapshot(),
			"PluginID":    pluginID,
			"Settings":    views,
			"Permissions": permViews,
		})(w, r)
	})

	mux.HandleFunc("POST /api/plugins/{id}/settings", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		pluginID := r.PathValue("id")
		// Same guard as the page (ut-docs#4135): no saving, and no
		// plugin_settings_saved audit row, for a plugin that isn't installed.
		if _, installed, err := repo.GetInstalledPluginVersion(r.Context(), pluginID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
			return
		} else if !installed {
			common.LocalizedError(w, r, http.StatusNotFound, "plugins.install.error.not_found")
			return
		}
		_ = r.ParseForm()
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings", err)
			return
		}
		isSecret, declaredSecret, declaredEndpoint, resolved := secretSettingCheck(r.Context(), d, pluginID)
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
		// Keys (never values) whose stored value changed, for the audit row:
		// a setting-bound grant moves with its setting (ut-docs#2899), so
		// which setting moved must be traceable.
		changedKeys := []string{}
		locale := httpx.ResolveLocale(w, r)
		// Validate any typed takeaway-overrides submission BEFORE the write
		// loop: rows are written in key order, so a mid-loop validation abort
		// would leave earlier keys' upserts committed with no generation bump
		// and no audit row — a sibling setting half-saved while the plugin
		// keeps serving cached answers (ut-docs#190 review finding).
		// Same rule for `type: "endpoint"` settings (ADR-0121 §2,
		// ut-docs#3328): a non-blank value must be http(s)://host[:port][/path]
		// — checked for every row before the first write. Blank clears it.
		for _, row := range rows {
			if !declaredEndpoint(row.Key) {
				continue
			}
			form, ok := r.Form["setting_"+row.Key]
			if !ok || len(form) == 0 {
				continue
			}
			if val := strings.TrimSpace(form[0]); val != "" && !plugins.ValidEndpointURL(val) {
				http.Error(w, fmt.Sprintf(httpx.T(locale, "plugins.settings.invalid_endpoint"), row.Key), http.StatusBadRequest)
				return
			}
		}
		// `type: "choice"` (ut-docs#2832): a submitted value must be one of
		// the manifest's options, or the value already stored (an older
		// install's free-text value is kept, never rewritten) — checked for
		// every row before the first write, like the endpoint rule above.
		manifest := settingsFormManifest(r.Context(), d, pluginID)
		for _, row := range rows {
			decl := manifest.SettingDecl(row.Key)
			if decl == nil || decl.Type != plugins.SettingTypeChoice {
				continue
			}
			form, ok := r.Form["setting_"+row.Key]
			if !ok || len(form) == 0 {
				continue
			}
			if val := strings.TrimSpace(form[0]); !decl.ChoiceAllows(val) && val != strings.TrimSpace(unwrapSettingValue(row.ValueJSON)) {
				http.Error(w, fmt.Sprintf(httpx.T(locale, "plugins.settings.invalid_choice"), row.Key), http.StatusBadRequest)
				return
			}
		}
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
				if n > 0 {
					changedKeys = append(changedKeys, row.Key)
				}
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
			// "keep the current value" rather than "clear it" — unless the
			// operator ticked "Remove saved key" (clear_setting_<key>,
			// ut-docs#2832), which clears it.
			if val == "" && isSecret(row.Key) && r.Form.Get("clear_setting_"+row.Key) != "1" {
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
			changedKeys = append(changedKeys, row.Key)
		}
		sort.Strings(changedKeys)
		_ = posRepo.InsertAudit(r.Context(), nil, getSessionUserID(r), "plugin", pluginID, "plugin_settings_saved",
			map[string]any{"changed": changed, "keys": changedKeys}, time.Now().UTC().Format(time.RFC3339), "")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s (%d)</span>`, httpx.T(locale, "plugins.settings.saved"), changed)
	})
}
