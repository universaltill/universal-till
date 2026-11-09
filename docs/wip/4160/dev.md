# Dev note — ut-docs#4160 asked options (`ui.settings.options.ask`)

Branches (nothing committed/pushed): universal-till `feat/4160-settings-options-ask`; ut-docs `docs/4160-settings-options-ask` (from origin/main).

## Files
universal-till, new: `internal/pages/plugin_ask.go` (boundedAskPlugin), `plugin_settings_options.go` (decoder, settingsAskValues, askSettingsOptions, mergeChoiceOptions, askedOptionsMessage, askOnKeys, endpoint), `plugin_settings_options_cache.go`, tests `plugin_ask_test.go`, `plugin_settings_options_unit_test.go`, `plugin_settings_options_test.go`, `internal/plugins/choice_asked_test.go`, `sdk/plugin/settings.go` + `settings_test.go`, `internal/plugins/testdata/settings_guest/main.go`, `e2e/seed_settings_plugin/main.go`, `e2e/tests/plugin-settings-options-4160.spec.ts`.
Changed: `internal/plugins/choice_settings.go` (AskedChoiceValueOK + consts), `event_dataclass.go`, `internal/pages/plugin_view.go` (askPluginUIAs now calls boundedAskPlugin), `plugin_settings_page.go`, `plugin_settings_fields.go` (showWhenHidden), `demo_mode.go` (new route denied in demo), `web/ui/pages/plugin_settings.html`, `web/public/app.css`, `web/locales/en.json`, `web/i18n-meta/en.meta.json`, `web/help/en/plugins.md`, `web/help/img/manifest.json` (make docs-shots regenerated; two unrelated drifting PNG sets reverted), `sdk/plugin/README.md`, pin tests (`wasm_event_dataclass_test.go`, `wasm_concurrency_test.go`, `event_dataclass_pin_test.go`).
ut-docs: `reference/plugin-views.md` (new "Settings asks" section, Not-yet-shipped rows #5961/#4158, intro), `reference/plugin-manifest.md` (asked options now drawn).

## Deviations (and why)
1. Route is `POST /api/plugins/{id}/settings/{key}/options`, not `.../settings/options/{key}`: the architect's pattern conflicts with `POST /api/plugins/entries/{plugin}/{key}/action` and panics the mux at startup.
2. Per-till rows: hx-post carries `?scope=register` so a per-till row's fragment is drawn from its own row (a key can have global + register rows, each drawn separately).
3. UX amendments A1-A9 implemented: 5 keys (loading/failed/retry/empty/saved_missing), message line outside the wrapper filled by `hx-swap-oob`, Try again button, aria-busy, pick-wins (beforeSwap), focus fix, `dir="auto"` on plugin labels, fragment-error handling, 400 ms CSS-delayed loading line. Message selection in `askedOptionsMessage`. `options_failed` text is the UX wording.
4. Initial ask is fired from a page script via `htmx:load` + a first scan one timer tick after DOMContentLoaded with a per-element `askOnce` guard. `htmx.onLoad` registered at DOMContentLoaded never fired for the first load (htmx fires its first load from a timer after init; found by the e2e).
5. Also had to classify the new route in `demo_mode.go` denied map (TestDemoRouteClassification) — not in the architect's list.
6. ut-docs review record NOT written (belongs to the Reviewer step).
7. Known/accepted: htmx 1.9 logs `htmx:sendAbort` as a console error when `hx-sync="this:replace"` aborts an in-flight ask (e.g. Save or a second provider change mid-ask). Harmless; the e2e waits for the in-flight ask before acting.
8. fa/ar/tr show the English text for the 5 new keys until Weblate (ADR-0160); lang-pack-drift advisory expected.

## Tests added
plugins: TestAskedChoiceValueOK; pins in TestEventDataClassRegistry, TestEventDataClass_AIAgentEmptySetAndOwnEvents, TestIsSalePathEvent, TestPageEventsHaveDataClasses.
pages: TestBoundedAskPlugin_{ReturnsAnswer,HandlerIgnoringContextReturnsByTimeout,PanicIsError,DeclinedIsNotAnswered}; TestDecodeSettingsOptionsAnswer (22 cases); TestSettingsAskValues{,_SwitchAndBlankNumber,_NoRowNoForm}; TestSettingsOptionsCache{,_EvictsOldestAtCap}; TestMergeChoiceOptions; TestAskedOptionsMessage; TestShowWhenHidden_AskedControllerIsLegitimate; handler: TestSettingsOptions_{GETDrawsWrapperAndAsksNothing,FragmentAsksWithUnsavedValuesAndEscapes,Cache,FailuresShowNoticeAndAreNotCached(timeout/error/panic/malformed),DeclinedAndDeniedAreEmptyNotFailures,EmptyAnswerIsCached,Refusals,SaveRule,422KeepsAskedPick,CurrentPickPostedIsKept}.
sdk: TestDispatchSettingsOptionsAsk, TestSettingsOptionsAnswer_NilIsEmptyList.
e2e (real WASM guest, chromium via /opt/pw-browsers symlinked as 1228): 6 tests in plugin-settings-options-4160.spec.ts (fill + escape + stored kept; provider switch w/o save; refused 33-option answer -> note, Try again, Save works; pick saves; 1024 en and 360 fa no overflow, retry >= 46px, console clean). Passed 4/4 consecutive runs after fixing test-side races.
TDD: tests written before code (plugins/pages compile-fail then pass); the e2e's first run failed and exposed deviation 4.

## Gates (universal-till)
gofmt -l: empty. go build ./...: ok. go vet ./...: ok. go test ./internal/... ./sdk/... : all ok (pages 348 s) after fixing TestDemoRouteClassification; sdk/plugin nested module ok. golangci-lint run ./...: 0 issues. Every guard-*/check script of the ci.yml build job (59 scripts, run directly): all exit 0, incl. i18n, i18n-meta, no-inline-handlers, rtl, no-showmodal, core-neutral, deadcode, help-*, htmx-loaded, plugin-settings-bump, docs-shots (regenerated). Not run: `guard-catimport-version.sh` (no catimport tags fetched; catimport untouched), `shellcheck` (binary not installed; no scripts changed), `*_test.sh` self-tests of guards, WebKit/locale audit workflows.

## Not done / for Tester+UX
- Touch-hardware check of the open native picker during a swap (UX §3) and drag-scroll: not testable here (headless desktop chromium only). Kiosk-mode (UT_KIOSK) pass and screenshots of S2/S4/S5 not taken. German +30% wrap not checked (no de strings).
- No help screenshot added (architect: none required).
- ut-docs review record; docs changes uncommitted on the ut-docs branch.
