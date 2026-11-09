# BA — ut-docs#4160 Plugin settings v2: asked options + Test connection (S5)

Card: ut-docs#4160 (parent epic #4023). Design: ADR-0121, amendment 2026-10-12
"plugin settings form v2", **S5** (`ut-docs/adr/0121-...md` ~l.1613-1663).
Dependency #4157 (S2 kinds, 422 re-render, batch save) is Done. Consumers
waiting: #4110 (AI model dropdowns + Test connection, p1), #4024 (tax-de TSE
test, p2). No human decision needed — see §7.

## 1. What already exists (verified in code, 2026-11-09)

Manifest schema (S5 fields parse, sign and validate — #4156, done):
- `ManifestSetting.OptionsAsk` / `DependsOn` — `universal-till/internal/plugins/manifest.go:324-325`
- `Manifest.SettingsTests` + `ManifestSettingsTest{ID,Label,Tab,Record}` — `manifest.go:90`, `:369-374`
- Validation: options_ask only on `choice`, 0..32 static options with options_ask,
  options_ask default ≤ 64 bytes no comma — `internal/plugins/choice_settings.go:104-125`;
  depends_on ≤ 8, declared, non-secret, needs options_ask — `internal/plugins/settings_v2.go:229-245`;
  settings_tests ≤ 4, id regex, unique, tab/record rules — `settings_v2.go:131-140`. Tests: `settings_v2_test.go:82-238`.
- ut-cloud mirror + upload checks already done: `ut-cloud/internal/signing/manifest.go:280`,
  `ut-cloud/pkg/manifest/settings_v2.go:48,160,175`. **No ut-cloud work in this card.**
- Docs: `ut-docs/reference/plugin-manifest.md:782` (settings_tests), `:798` (options_ask/depends_on);
  `:765-768` says asked options/tests (#4160) "are not drawn yet".

Settings page (S2 — #4157, done):
- `GET /plugins/{id}/settings` — `internal/pages/plugin_settings_page.go:814`;
  `POST /api/plugins/{id}/settings` — `:848` (gate `canPerform(...,"plugin_management")`,
  fail-closed on unresolved manifest `:870-879`, validate-all-then-write `:886`, 422 form
  re-render `:891-906`, one commit `:908`, audit `plugin_settings_saved` `:924`).
- Choice drawn as `<select>` from **static** options; unknown stored value kept selectable
  — `buildSettingForm` `plugin_settings_page.go:134-147`; template
  `web/ui/pages/plugin_settings.html:154-156`; form `#ps-form` `:146` (`hx-target="this"`).
- Field kinds/parsing: `internal/pages/plugin_settings_fields.go` (`settingFieldKind:32`, `fieldError:187`).
- Batch-save seam: `PluginRepo.SavePluginSettingsBatch` — `internal/data/plugin_repo.go:1026`
  (one tx, one `onSettingsChanged` → `EventBus.BumpGeneration`, `ipc.go:377`).

Ask plumbing:
- `EventBus.AskPlugin(ctx, pluginID, event, payload)` — `internal/plugins/ipc.go:857`: targets one
  plugin, checks `events:receive` per call, returns `(raw, ok, err)`; empty answer = `ok=false`.
- Subscription = manifest `hooks[]` (`ManifestHook`, `manifest.go:392`); `ui` is a reserved core root
  (`manifest_abi3.go:131`) so no plugin can publish these names.
- `isSalePathEvent` excludes every `ui.*` event — `internal/plugins/wasm_concurrency.go:43` (no change needed).
- Bounded UI-ask precedent with panic recovery + hard timeout: `askPluginUIAs`
  `internal/pages/plugin_view.go:566-605` (`pluginViewTimeout` 5 s, `:38`). It also checks `ui:page` —
  S5 must **not** (ADR: gate is `events:receive` only).
- WASM per-call deadline: 2 s, or 10 s for a plugin holding `net:*`/`tcp:*` —
  `internal/plugins/wasm_runtime.go:206-214,249-250`. The page timeout (5 s / 10 s) is the outer bound.
- Own-locale key check precedent: `pluginOwnLocaleKeys` — `plugin_view.go:63`.
- Contract doc home: `ut-docs/reference/plugin-views.md` "## The two asks" (`:66`), "Not yet shipped" (`:805`).
- Guest SDK: `universal-till/sdk/plugin` (`Run`/`Handlers`, typed request precedent
  `ImportRequest` in `sdk/plugin/fileimport.go:19`, README).

## 2. What is missing for S5 (the gap this card closes)

G1. **No ask is ever sent.** Nothing in `internal/` sends `ui.settings.options.ask` /
    `ui.settings.test.ask` (grep: only manifest validation references the fields).
G2. **options_ask values cannot be saved today.** Save validation accepts a choice only if
    `ChoiceAllows` (static options) or equal to the stored value — `plugin_settings_page.go:689`.
    A plugin-supplied model id is refused with `plugins.settings.error.choice`. S5 rule:
    with `options_ask`, accept any value ≤ 64 bytes with no comma.
G3. **ai.agent plugins would never get the asks.** `eventDataClasses`
    (`internal/plugins/event_dataclass.go:40-55`) fails closed for unregistered events;
    `WasmRuntime.handleEvent` withholds them from an `ai.agent` plugin — exactly #4110's AI plugin.
    Both events must be registered with an empty class set (payload is the plugin's own settings).
G4. No Test connection buttons, endpoint, status line, i18n keys, help text.
G5. No options cache, no depends_on re-ask wiring.
G6. Docs: contract in `reference/plugin-views.md`, manifest page "not drawn yet" note,
    help topic `web/help/en/plugins.md`, guest SDK types + README.

Out of reach of this card (dependencies still open — do NOT design around them):
- `record_id` / `record: true` tests and record-scoped options need `list` (#4158, open).
- Placing a test button in its `tab` needs tabs (#4666, open, admin-review).
- A setup step's `test` needs the setup flow (#4161, open).

## 3. The contract (from ADR S5; Architect must not change it)

Common:
- Sent with `EventBus.AskPlugin` to the owning plugin only; never broadcast.
- Plugin subscribes via `hooks[]` (`{"event":"ui.settings.options.ask","action":"settings"}`);
  delivery gated by `events:receive` only — no new permission; plugin's own `http:*` grants bound reach.
- Not sale-path (`ui.` prefix); no sale-path phase. ADR-0041 failure policy **`skip`**, with an
  inline, non-blocking notice where the operator acted. A failed ask never blocks the page.
- Neither payload ever carries a secret (declared `type:"secret"` or `IsSecretSettingKey`-named).

`ui.settings.options.ask` (for a `choice` with `"options_ask": true`):
- Payload: `{"key": "<setting key>", "record_id"?: "<16 hex>", "values": {…}}`.
  `values` = form's **current** non-secret values incl. unsaved ones: the declared top-level
  settings (record siblings when `record_id` — deferred to #4158). Excluded: declared secrets,
  heuristic-secret keys, and (BA recommendation) `list`/`map` values (they can hold sealed record
  secrets and are not one string).
- Answer: `{"options":[{"value":"…","label":"…"}]}` — ≤ 32 entries; value ≤ 64 bytes;
  label ≤ 128 bytes **plain text, escaped by core** (not an i18n key). Empty/declined answer
  (`ok=false`, `{}`, `{"options":[]}`) = empty list, **not** an error.
- When: when the page is drawn, and again when a field named by the choice's `show_when` or its
  `depends_on` changes. Timeout **5 s**.
- Failure (timeout, error, crash, offline, malformed answer): stored value stays selectable;
  a notice under the field says options couldn't be loaded.
- Cache: in memory, per plugin + key + hash(values), **10 min TTL, ≤ 64 entries per plugin**.
- Save: with `options_ask`, static `options` optional; save accepts any value ≤ 64 bytes, no comma.
  A stored value is never rewritten because a later answer lacks it.

`ui.settings.test.ask` (button declared in `settings_tests: [{id,label,tab?,record?}]`, ≤ 4):
- Press → core **saves the form first** (same validation + batch save as POST settings);
  a failed save (422) means **no test**.
- Payload: `{"test": "<id>", "record_id"?: "<16 hex>"}`. Plugin reads saved values, secrets
  included, via `settings_get` — the secret never crosses the page or this payload.
- Answer: `{"ok": bool, "message"?: "<plugin i18n key>", "field"?: "<setting key>"}`.
- Timeout **10 s**. UI: "Testing…" → "Connected ✓" or the translated reason, in a
  `role="status"` line, never a modal. Timeout / crash / no answer → "Couldn't run the test";
  saved settings stay saved.

## 4. Requirements

R1 Register both event names in `eventDataClasses` with an empty class set; ai.agent plugins
   receive them without any `ai.send.*` grant.
R2 A shared, bounded settings-ask helper (pattern of `askPluginUIAs`, minus `ui:page`):
   hard timeout even if the handler ignores ctx, panic → failure, never holds a request
   goroutine beyond its timeout.
R3 Strict answer decoders (pluginview precedent): an answer violating any bound (count, byte
   lengths, empty value, value with comma, duplicate values, unknown top-level shape) is a
   **failure** (notice), not partially applied. Labels are escaped (`html/template`), never
   `template.HTML`.
R4 Options load must not delay the page: the GET responds without waiting on any plugin
   (BA recommendation: htmx fragment per options_ask field, triggered on load and on change
   of its `show_when`/`depends_on` controllers, posting current non-secret form values;
   Architect picks the shape). No-JS: static options + stored value remain usable.
R5 Selecting from asked options: stored value always present and selected; asked options
   follow; static options (if any) merged without duplicates.
R6 Save rule G2 for options_ask choices (≤ 64 bytes, no comma, `required` still enforced);
   static-only choices unchanged.
R7 Cache: successful answers only (empty list counts as success); failures never cached;
   key includes the plugin's installed version; a save of that plugin's settings drops its
   entries (a new API key must refetch the model list — ADR says "may be cached", this only
   narrows it). Bounded: 10 min, 64 entries/plugin, LRU/oldest evicted.
R8 Test buttons: drawn for each `settings_tests` entry without `record`; until #4666 ships
   they sit in one block after the fields (inside `#ps-form`), labelled by their i18n `label`.
   A `record: true` test is not drawn (no list UI yet). Button disabled while running.
R9 Test endpoint (e.g. `POST /api/plugins/{id}/settings/test/{test_id}`): same gate
   (`plugin_management`), installed-check, fail-closed manifest rule and validation as save;
   unknown/record test id → 404; save, then ask; response renders the form (saved values) plus
   the status line. Writes the usual `plugin_settings_saved` audit row.
R10 Test result: `message` shown only if it is a key in the plugin's own locale bundle,
   else the generic text for ok/failed; `field` marked only if it is a declared, drawn
   setting (`aria-invalid` + `aria-describedby` → status line); otherwise ignored.
R11 i18n: new core keys in `web/locales/en.json` + `web/i18n-meta/en.meta.json` (e.g.
   `plugins.settings.options_failed`, `plugins.settings.test.running`, `.ok`, `.failed`,
   `.unavailable`); English only (ADR-0160). RTL-safe CSS.
R12 Docs in the same branch: contract section for both asks in `reference/plugin-views.md`
   (payloads, answers, bounds, timeouts, cache, failure policy, ai.agent note); remove "#4160
   not drawn yet" from `plugin-manifest.md:765-768`; help topic `web/help/en/plugins.md`
   (+ screenshots via `make docs-shots`); guest SDK typed request/answer structs
   (`SettingsOptionsRequest/Answer`, `SettingsTestRequest/Answer`) + README section.
R13 Offline-first: no ask is on the checkout path; the till never makes a network call itself
   — only the plugin does, under its own grants.

## 5. Acceptance criteria (Tester-checkable)

Options ask
1. A plugin with `hooks[ui.settings.options.ask]` + `events:receive` and a choice with
   `options_ask` gets exactly one ask per draw with payload `{key, values}`; the dropdown shows
   its answered options (labels escaped: `<b>x</b>` renders as text).
2. Without `events:receive` (or without the hook) no answer is used: dropdown shows the
   stored value only; a `denied` dispatch audit row exists; page is 200.
3. `values` contains no declared `secret`, no `IsSecretSettingKey` key (e.g. `api_key`),
   no `list`/`map` value — asserted on the raw payload with a secret stored and typed.
4. Changing a `depends_on` (or `show_when` controller) field re-asks with the **unsaved**
   new value in `values`.
5. Plugin sleeps > 5 s: the GET page returns without waiting (≤ 1 s with a fake plugin); the
   options fragment answers by ~5 s with the stored value selected and the
   "options couldn't be loaded" notice; Save still works.
6. Plugin errors / panics / is uninstalled-mid-request / answers malformed (33 options, 65-byte
   value, value with comma, 129-byte label): same as 5 — notice, stored value kept.
7. Empty or declined answer: empty list (stored value still selectable), **no** notice.
8. Second draw within 10 min with the same values: no second ask (cache hit); different
   values → new ask; after a save of that plugin's settings → new ask; > 64 distinct entries
   for one plugin → oldest evicted (unit test); failures are not cached.
9. Save of an options_ask choice: any value ≤ 64 bytes w/o comma is stored as a JSON string;
   65 bytes or a comma → 422 `plugins.settings.error.choice`; `required` empty → 422.
   A static-only choice still refuses values outside its options.
10. A stored value absent from a later answer stays stored and selected (never rewritten).
11. An `ai.agent` plugin with no `ai.send.*` grants receives both asks (G3).

Test connection
12. Each non-record `settings_tests` entry renders a button with its translated label;
    `record: true` entries render nothing; no `showModal` (guard-no-showmodal passes).
13. Press with an invalid field → 422 form, field errors, **no** ask sent.
14. Press with valid edits → values saved (one `plugin_settings_saved` audit row), then ask
    payload is exactly `{"test":"<id>"}` — no setting values, no secret.
15. `{ok:true}` → "Connected ✓" in a `role="status"` element; `{ok:false, message:<own key>,
    field:<declared key>}` → translated message, that field `aria-invalid`.
16. `message` not in the plugin's own bundle, or `field` undeclared → generic text, no marking.
17. Plugin sleeps > 10 s, crashes, or declines → "Couldn't run the test" by ~10 s; the saved
    values remain stored (re-GET shows them).
18. Unknown test id → 404; non-manager → 403; not-installed plugin → 404; unresolved manifest
    → 500 and nothing saved (ADR-0082 fail-closed).
19. Offline (plugin's HTTP fails): plugin's own failure answer or timeout shows the inline
    result; nothing else on the till is affected; a sale completes meanwhile.

Both / docs
20. Neither event takes a reserved sale-path slot (`isSalePathEvent` false — unit test).
21. `reference/plugin-views.md` documents both asks; SDK has typed structs + README;
    help topic updated; new keys have meta; all CI guards in `ci.yml` build job green.
22. Driven run (1024×600 en, 360px fa RTL): dropdown fills from a test plugin; Test shows
    Testing… then result; screenshots looked at.

## 6. Sizing — split recommended

One cycle is too big: two independent UI flows, a cache, a new endpoint, docs + SDK + help,
plus an ai.agent gate fix. #4157 (one S5-sized slice) was complexity:hard, and #4666 hit
budget-exceeded. Split into two sub-cards of #4023 (each complexity:medium):

**4160a — Asked options (`ui.settings.options.ask`)** — BUILD FIRST.
Scope: R1 (both events registered, so 4160b needs no gate work), R2 shared helper, R3 options
decoder, R4–R7, R11 (options keys), R12 (contract section for both events' common rules + the
options ask; SDK `SettingsOptions*`; help), AC 1–11, 20, 21-22 (options part).
Why first: it fixes a live gap (G2 — an options_ask manifest is installable today but its
values can't be saved), lands the shared plumbing (G3, helper), and unblocks #4110's model lists.

**4160b — Test connection (`ui.settings.test.ask`)** — `blocked:dep` on 4160a.
Scope: R8–R10, test decoder, R11 (test keys), R12 (test ask contract, SDK `SettingsTest*`,
help), AC 12–19, 21-22 (test part). Unblocks #4024's TSE test and #4110's Test button.

Follow-ups to attach (not new work invented here): record-scoped options/tests (`record_id`,
`record: true`) → add to #4158's scope; test button placement in its tab → #4666; setup
step `test` → #4161. #4110 and #4024 keep `blocked:dep` until the relevant sub-card is Done.

## 7. Decisions

Human decisions: **none** — ADR-0121 S5 decided events, payloads, timeouts, cache bounds,
failure policy; no new permission, secret, pricing or legal question.

Architect calls (engineering, not escalations):
- Fragment endpoint shape for options (R4) and test endpoint path (R9).
- `values` value types: BA recommends the S2 stored shape (bool/int/bps/minor units/string, as
  `settings_get` returns) for parseable values, omitting unparseable ones — consistent for the
  plugin. Raw strings are the simpler alternative; pick one and freeze it in the contract.
- Whether to add `locale` to the options payload (labels are plain text, so a plugin can only
  localise them if told the locale; `ui.view.ask` carries `locale`). Additive; if added, record
  it in the contract (and a one-line note on ADR-0121 S5 per ADR-0007).
- Cache invalidation hook (R7): via the existing settings-save path / installed version.
