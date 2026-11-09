# Architect — ut-docs#4160 (rescoped): asked options, `ui.settings.options.ask` (ADR-0121 S5)

Scope: S5 options half only. Test connection = #5961 (reuses `boundedAskPlugin` at 10 s,
adds its own data-class line); `record_id` = #4158. BA §3 contract binding; 4160b ACs ignored.

## 0. Decisions in one place

- **Placement:** `universal-till` core (neutral UI-ask seam) + ut-docs reference docs. No
  ut-cloud change (fields already mirrored/signed, #4156); no SQL / `internal/data` change.
- **ADR: none needed.** S5 decides event, payload, bounds, 5 s, cache, failure policy, save
  rule. Two contract details go in `reference/plugin-views.md` (S5's ADR-0039 home):
  1. `values` encoding = `map[string]string`, each value is **exactly what `settings_get`
     would return once saved** (text/choice/endpoint: plain string; bool: `"true"`/`"false"`;
     number/percent/money: canonical integer text such as `"750"`, bp/minor units). A
     submitted number that doesn't validate, or is blank, is **omitted**.
  2. **No `locale` in the payload** (not in S5; labels are provider data). Additive later.
- **Cache narrowing (ADR: "may be cached"):** cache real answers only (incl. `{"options":[]}`);
  no answer (`ok=false`) and failures are never cached, so a new grant works at once. Key
  includes the installed version; a save of that plugin's settings drops its entries.
- **Page never waits on a plugin:** GET draws static options + stored value (no-JS works);
  asked options arrive by an htmx fragment per asked choice.

## 1. Files and changes (TDD order)

### 1a. `internal/plugins/choice_settings.go` (consts at :19-20)
```go
// AskedChoiceValueOK: a value a plugin may answer for, and save may store in, an
// options_ask choice (ADR-0121 S5): 1..64 bytes, valid UTF-8, no comma, no control
// character, equal to its own TrimSpace (save trims).
func AskedChoiceValueOK(v string) bool
const MaxAskedOptions = maxSettingOptions          // 32
const MaxAskedOptionValueBytes = maxSettingOptionValue // 64
const MaxAskedOptionLabelBytes = 128
```
Do **not** change manifest validation of `default_value` (:116-120) — tightening could
refuse already-signed manifests. Table test: "", 64 B, 65 B, "a,b", " a", "a\n", bad UTF-8.

### 1b. `internal/plugins/event_dataclass.go:40-55`
Add `"ui.settings.options.ask": {},` with comment "the plugin's own non-secret settings".
Pin it in `TestEventDataClassRegistry` (`wasm_event_dataclass_test.go:397`) and
`internal/pages/event_dataclass_pin_test.go:14`; test an `ai.agent` plugin without
`ai.send.*` receives it (copy :241/:389); assert `isSalePathEvent` (`wasm_concurrency.go:43`,
unchanged) is false for it.

### 1c. NEW `internal/pages/plugin_ask.go` — shared bounded ask (R2)
Extract the goroutine/select body of `askPluginUIAs` (`plugin_view.go:554-602`):
```go
// boundedAskPlugin asks exactly pluginID on the shared bus, bounded by timeout even if
// the handler ignores ctx; a panicking handler is an error. answered=false: nobody
// answered (no hook, events:receive refused, data class withheld, declined/empty).
// No permission check of its own: AskPlugin gates on events:receive.
func boundedAskPlugin(ctx context.Context, d *common.Deps, pluginID, event string,
    payload any, timeout time.Duration) (raw []byte, answered bool, err error)
```
`askPluginUIAs` keeps `ui:page`/operator/agent-ctx and calls it (`!answered` →
`errPluginNoAnswer`; same timeout error); plugin_view tests unchanged. `plugin_ask_test.go`:
ctx-ignoring handler returns by timeout; panic → error; declined → answered=false.

### 1d. NEW `internal/pages/plugin_settings_options.go`
```go
const settingsOptionsAskEvent = "ui.settings.options.ask"
var settingsOptionsTimeout = 5 * time.Second // var: tests shorten it
const settingsOptionsMaxAnswerBytes = 16 << 10
const settingsOptionsMaxFormBytes = 64 << 10
type settingsOptionsAsk struct { // payload; #4158 adds RecordID `json:"record_id,omitempty"`
    Key    string            `json:"key"`
    Values map[string]string `json:"values"`
}
type askedOption struct{ Value, Label string }
// decodeSettingsOptionsAnswer: strict. raw ≤ 16 KiB; one JSON object, DisallowUnknownFields,
// no trailing data; only key "options" (missing/null/[] = empty list); ≤ 32 entries;
// each {value,label} only; AskedChoiceValueOK(value); label ≤ 128 B, valid UTF-8, no
// control chars ("" → label = value); duplicate values refused. Any violation = error
// (whole answer rejected, never partially applied). raw "null" = empty list.
func decodeSettingsOptionsAnswer(raw []byte) ([]askedOption, error)
// settingsAskValues: the form's current non-secret top-level values (unsaved ones
// included) as settings_get would return them once saved. Only manifest-declared keys;
// skips isSecret(key) (declared secret OR IsSecretSettingKey), list, map. Source:
// submitted value when the form carries it (bool via submittedBool; number kinds via
// validateNumberField → omit on error or "null"; others TrimSpace), else
// storedControlValues(rows)[key]; a key with neither is omitted.
func settingsAskValues(m *plugins.Manifest, form url.Values, rows []data.PluginSettingRow,
    isSecret func(string) bool, decimals int, locale string) map[string]string
// askSettingsOptions: cache → boundedAskPlugin → decode. failed=true on timeout, error,
// panic, malformed answer (notice). !answered → (nil, false) and NOT cached.
func askSettingsOptions(ctx context.Context, d *common.Deps, pluginID, version, key string,
    values map[string]string) (opts []askedOption, failed bool)
// mergeChoiceOptions: dropdown entries for a choice (R5). current=="" → "not set" entry
// (plugins.settings.secret_empty) selected; current not in asked∪static → {current,
// current, Selected, Unknown: !decl.OptionsAsk} first; then asked (plain-text label, in
// answer order); then static options not already present (label via httpx.T).
func mergeChoiceOptions(decl *plugins.ManifestSetting, current string, asked []askedOption,
    locale string) []choiceOptionView
```
Log failures with `logging.L().Infof` (plugin id, key, error) — **never log `values`**.

### 1e. NEW `internal/pages/plugin_settings_options_cache.go`
```go
type settingsOptionsCache struct {
    mu  sync.Mutex
    now func() time.Time
    byPlugin map[string]map[string]cachedOptions // pluginID → cache key → entry
}
type cachedOptions struct { opts []askedOption; at time.Time }
const settingsOptionsTTL = 10 * time.Minute
const settingsOptionsMaxPerPlugin = 64
func newSettingsOptionsCache(now func() time.Time) *settingsOptionsCache
func (c *settingsOptionsCache) get(pluginID, k string) ([]askedOption, bool) // expired = miss; returns slices.Clone
func (c *settingsOptionsCache) put(pluginID, k string, opts []askedOption)   // prune expired; at cap evict oldest `at`
func (c *settingsOptionsCache) dropPlugin(pluginID string)
// key = version + "\x00" + settingKey + "\x00" + hex(sha256(json.Marshal(values))) (map keys sort)
func settingsOptionsCacheKey(version, settingKey string, values map[string]string) string
var sharedSettingsOptionsCache = newSettingsOptionsCache(time.Now)
```
Outer map bounded: only installed plugin ids reach `put`.

### 1f. `internal/pages/plugin_settings_page.go`
- `settingForm` (:94): `Ask bool`, `AskOn string` (deduped DependsOn ∪ ShowWhen.Key, comma-joined).
- `buildSettingForm` choice branch (:134-147): replace with `mergeChoiceOptions(decl, value,
  nil, locale)` — static-only output must stay byte-identical (existing choice tests pin it).
- `buildPluginSettingViews` (:492): accept a submitted value for an options_ask choice when
  `AskedChoiceValueOK(trim(sub))` (so a 422 re-render keeps the operator's pick).
- `validatePluginSettings` choice case (:684-691): with `decl.OptionsAsk`, accept
  `val == stored || AskedChoiceValueOK(val)`; `required && val==""` still 422; otherwise
  `plugins.settings.error.choice`. Static-only branch unchanged (AC 9).
- `showWhenHidden` (`plugin_settings_fields.go:248`): add `&& !ctl.OptionsAsk` to the
  "value outside options hides nothing" exception (an asked value is legitimate).
- POST save (:908): on success with `len(changedKeys) > 0` →
  `sharedSettingsOptionsCache.dropPlugin(pluginID)`. Register 1g's route in :755.

### 1g. Endpoint — `POST /api/plugins/{id}/settings/options/{key}` (htmx fragment)
POST: htmx sends the enclosing `#ps-form` in the body (not URL/logs). No CSRF tokens in this
app (SameSite=Lax session); **add** `sameOriginRequest(r)` (`authz.go:70`) → 403 (egress).
Order: `canPerform(d,r,"plugin_management")` else 403 `common.error.manager_or_admin_required`
→ same-origin else 403 → `repo.GetInstalledPluginVersion` (version kept for the cache key;
not installed → 404 `plugins.install.error.not_found`) → `InstalledManifest` (error → 500;
nil/undeclared key/not choice/not OptionsAsk → 404) → `r.Body = http.MaxBytesReader(…64 KiB)`,
`ParseForm` → `ListPluginSettings`; no row for key → 404 (the form draws rows only) →
`values := settingsAskValues(...)` → `opts, failed := askSettingsOptions(...)` →
current = trimmed `setting_<key>` if posted and (`AskedChoiceValueOK` or == stored), else stored
→ build that one `pluginSettingView` (Label, Help, PerTill, Notice, Required, Ask, AskOn,
Options = `mergeChoiceOptions(decl,current,opts,locale)`, `AskFailed = failed`,
`Invalid = r.Form.Get("ps_ask_invalid")=="1"`) → `httpx.RenderBlock("ui/pages/plugin_settings.html",
"plugin_settings_choice", view)` with 200 (also on failure: notice is content, not an error).

### 1h. `web/ui/pages/plugin_settings.html`
- Move the choice branch (:154-156) into `{{ define "plugin_settings_choice" }}` used by the
  form and the fragment. For `.Ask` wrap it:
  `<div class="ps-ask" data-ps-ask data-ps-ask-on="{{ .AskOn }}" hx-post="/api/plugins/{{ $.PluginID }}/settings/options/{{ .Key }}" hx-trigger="ps-reask" hx-target="this" hx-swap="innerHTML" hx-sync="this:replace"{{ if .Error }} hx-vals='{"ps_ask_invalid":"1"}'{{ end }}>`
  (pass PluginID into the view, since `$` differs in the block). Inside: notice `<p>`, `<label>`
  + `<select>` (unchanged attributes; `aria-invalid`/`aria-describedby` when `.Error` or
  `.Invalid`), then
  `{{ if .AskFailed }}<p class="muted" role="status" id="ps-opts-msg-{{ .Key }}">{{ T "plugins.settings.options_failed" }}</p>{{ end }}`.
  The field error `<p id="ps-err-…">` (:195) stays outside the wrapper.
- Script: extend the show_when IIFE (:84-104), no new strings, no `hx-on`/inline handlers
  (guard-no-inline-handlers), no `hx-trigger` filter expressions (need eval under CSP):
  `htmx.onLoad(elt)` → for each `[data-ps-ask]` in `elt` not inside `[hidden]` →
  `htmx.trigger(el,'ps-reask')` (fires on first load, after a save swap and a 422 swap — not
  after its own swap, whose children carry no `data-ps-ask`). In `onEdit` on **`change` only**
  (not `input`), after `apply(form)`: key = name minus `setting_`; trigger `ps-reask` on every
  visible `[data-ps-ask]` whose `data-ps-ask-on` list contains it.

## 2. Guest SDK — NEW `sdk/plugin/settings.go` (+ `settings_test.go`)
```go
const SettingsOptionsAskEvent = "ui.settings.options.ask"
type SettingsOptionsRequest struct {
    Key      string            `json:"key"`
    RecordID string            `json:"record_id,omitempty"` // #4158; empty today
    Values   map[string]string `json:"values"` // as settings_get returns; no secrets
}
type SettingsOption struct { Value string `json:"value"`; Label string `json:"label"` }
type SettingsOptionsAnswer struct { Options []SettingsOption `json:"options"` }
```
Test: `Dispatch` round-trip encodes the answer as `{"options":[…]}`. README: one bullet after
the file-rows bullet (hook, decode, answer bounds, labels are plain text, nil = empty list).

## 3. i18n (`web/locales/en.json` + `web/i18n-meta/en.meta.json`, English only)
| key | en | meta |
|---|---|---|
| `plugins.settings.options_failed` | `Couldn't load the choices from the plugin. The saved choice is kept and you can still save.` | review standard; "Note under a plugin settings dropdown whose list the plugin supplies, when asking the plugin failed (timeout, offline, error). The stored value stays selectable." `max_length` ~140 |
That is the only new key (no "loading" text: the select stays usable while it loads). New
key → lang-pack-drift warning on the PR is expected/advisory.

## 4. Docs (same branch set)
- ut-docs `reference/plugin-views.md`: new `## Settings asks` before `## Not yet shipped`
  (:805): common rules (AskPlugin to the owner only, hook + `events:receive`, no new
  permission, not sale-path, ai.agent: no data class, ADR-0041 `skip` + inline notice,
  never a secret in payload), then `### ui.settings.options.ask`: payload + `values` encoding
  (§0.1), no locale, answer bounds (§1d), empty/declined = empty list, triggers (draw;
  change of `show_when`/`depends_on` field, on `change` i.e. blur for text), 5 s, cache rules
  (§0), save rule, never-rewritten; example JSON. Add `ui.settings.test.ask` → #5961 and
  `record_id` → #4158 rows to "Not yet shipped". Update the intro line :16 if it lists asks.
- ut-docs `reference/plugin-manifest.md:765-768`: "asked options (#4160) … not drawn yet" →
  options are drawn (link the new section); tests stay "not yet" (#5961).
- `web/help/en/plugins.md` step 4, 2-3 sentences: some drop-downs are filled by the plugin
  (e.g. models), refresh when a field they depend on changes, keep the saved choice with a
  note if the plugin can't be reached; a new key's list appears after Save. No new route,
  no screenshot required.
- Review record: ut-docs `code-reviews/<date>-settings-options-ask-4160.md`.
## 5. Tests (write first)
Unit (`internal/pages`): (1) decoder table: ok; `{}`/`null`/`[]` → empty; 33 entries; 65 B
value; comma; `" a"`; empty value; 129 B label; `\n` label; dup values; unknown field (top
and entry); trailing data; > 16 KiB. (2) `settingsAskValues`: declared secret, `api_key`,
list, map excluded even when typed; unsaved values used; switch; percent "7,5" → "750";
invalid number and undeclared key omitted. (3) cache with fake clock: hit, TTL, values/
version change → miss, 65th put evicts oldest, `dropPlugin`, copy returned.
(4) `mergeChoiceOptions`: static-only output == old; dedupe; current kept; `Unknown` false
for OptionsAsk. (5) `showWhenHidden` with an asked controller.
Handler (new `plugin_settings_options_test.go`; harness like `setupV2Plugin`
`plugin_settings_v2_test.go:53` with its own manifest: `provider` choice a/b, `model`
choice `options_ask` + `depends_on:["provider"]`, `api_key` secret, `items` list; fake plugin
= `SubscribeWithHandler` + `plugin_hooks` row + `events:receive` grant as
`plugin_slot_test.go:51-77`; shortened `settingsOptionsTimeout`):
6 GET renders `data-ps-ask`, stored value selected, sends **no** ask. 7 fragment: one ask,
payload `{key, values}` with the **unsaved** provider, no secret/list (secret typed too).
8 label `<b>x</b>` escaped. 9 same values → no 2nd ask; after a save → asks again.
10 timeout/error/panic/malformed → ≈timeout, notice, stored selected, not cached.
11 declined / no `events:receive` → no notice, not cached, `denied` dispatch audit row.
12 403 non-manager; 403 `Sec-Fetch-Site: cross-site`; 404 not installed / static choice /
unknown key; 500 corrupt manifest. 13 save: 64 B stored as JSON string; 65 B / comma →
422 `plugins.settings.error.choice`; required empty → 422; static choice unchanged; stored
value missing from a later answer still selected. 14 422 re-render keeps the asked pick.
E2E (supported: `seed_plugin_views` installs a real WASM guest):
- NEW guest `internal/plugins/testdata/settings_guest/main.go` (decodes
  the SDK request type): provider `a` → 2 options (one
  label `<b>Big</b>`), `b` → 1, `c` → 33 options (malformed → notice); fixture provider
  choice has static a/b/c.
- NEW seed `e2e/seed_settings_plugin/main.go` (copy of `seed_plugin_views`): builds guest,
  writes `manifest.json` (settings above; check `Manifest.RefuseOnTill` re `hosts`), DB rows,
  hook, `events:receive`, seeded values. Generic names only.
- NEW `e2e/tests/plugin-settings-options-4160.spec.ts`, own till via `extraSeeds`: list fills;
  label escaped; provider switch refreshes without save; `c` → notice, stored kept, Save
  works; 1024×600 en + 360 px fa, no horizontal overflow; `watchConsole` clean.

## 6. CI guards likely to bite (build job)
guard-i18n/-i18n-meta (meta for the key; no literal text in JS); guard-no-inline-handlers
(no `hx-on`/`onclick`); guard-no-showmodal; guard-rtl-logical-css; guard-core-neutral
(generic fixture names); guard-help-topics/-drift; guard-fixed-dates; guard-deadcode-baseline
(no #5961 code early); guard-e2e-* (new seed); golangci `unused`. Advisory only:
lang-pack-drift, locale-render-audit (new key deferred by design).

## 7. Size
**One cycle** (medium): ~5 small Go units, one route, one block, ~20 lines JS, SDK, docs.
If short of budget, cut only the Playwright spec (+ guest/seed) to a follow-up; Go handler
tests cover AC 1-11. Never cut docs, help or SDK types.

## 8. Out of scope
Test ask + its data class (#5961); `record_id` (#4158); tabs (#4666); setup (#4161);
`locale` in payload; singleflight for concurrent identical asks (bounded by hx-sync +
WASM deadline — note in review).
