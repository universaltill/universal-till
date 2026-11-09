# Tester — ut-docs#4160 asked options (`ui.settings.options.ask`)

Independent verification of the uncommitted universal-till `feat/4160-settings-options-ask`
(+ ut-docs `docs/4160-settings-options-ask`). Nothing committed/pushed/commented.

**Verdict: one production defect (D1, S1 loading delay) → bounce to Dev. Everything else passes or
is honestly "emulated / not verifiable here".**

## What was run
| Run | Result |
|---|---|
| Mutation checks (17 mutants, `scratchpad/4160/mut.sh`: mutate → targeted `go test` → restore) | 16 killed at first; 1 survived (the 16 KiB answer limit). Test fixed (below); now 18/18 killed |
| `npx playwright test tests/plugin-settings-options-4160.spec.ts --trace off --workers=1` (own port band 9391) | **6/6 passed** (37 s), incl. 1024 en + 360 fa |
| Own driven walk of ux.md §7 (`drive.js`, `nojs.js`, `probe*.js`) on two real tills: windowed (:9601) and `UT_KIOSK=1` (:9602), seeded with seed_faq + seed_demo + seed_settings_plugin, then the guest swapped for a tester-only build (via `go build -overlay`, repo untouched) that adds `slow` (1.5 s), `hang` (6 s), `long` (128-byte labels) providers, plus a `show_when`-hidden asked field `extra_model` that depends on a text field | see §7 table |
| Guards: data-access, i18n, i18n-meta, no-showmodal, rtl-logical-css, help-topics, kiosk-engine, no-inline-handlers | all rc=0 |
| `go test ./internal/plugins/... ./internal/pages/... -race -count=1` | run 1: no `DATA RACE`, but `internal/plugins` and `internal/pages` hit the **default 10 m timeout** (the machine was shared with my e2e/browser runs; goroutines at the time were in unrelated tests such as TestPersistManifest_FiscalSign…/TestBackupUpload_Refusals). Run 2 with `-timeout 50m`: same two packages timed out again at 50 m, 0 `DATA RACE`. At the timeout the running tests were unrelated and short (TestPluginNetworkNeeds 2 m 5 s, TestRejoinSends… 1 s), so the packages are just too slow under `-race` on this 4-core box; nothing hung. All other subpackages passed under -race. **Substitutes:** (a) `-race` on every 4160 test plus the settings/demo-route/data-class tests (`pages -run 'SettingsOptions|BoundedAsk|SettingsAsk|MergeChoice|AskedOptions|ShowWhenHidden|DecodeSettings|PluginSettings|PageEventsHaveDataClasses|DemoRoute'`, `plugins -run 'AskedChoice|EventDataClass|IsSalePath|ChoiceSetting'`): **ok**, no race. (b) Full `go test ./internal/plugins/... ./internal/pages/...` without -race: **all ok** (plugins 764 s, pages 358 s). Full-package -race was **not** completed; CI or a bigger box should run it |

## Mutation checks (production line broken → test fails → restored)
`git diff` of tracked files was byte-identical to the dev baseline afterwards; untracked files were
sha256-identical except the one test file I changed on purpose.

| # | Mutant | Killed by |
|---|---|---|
| M1 | `AskedChoiceValueOK` allows 65 bytes | TestAskedChoiceValueOK |
| M2 | comma allowed | TestAskedChoiceValueOK |
| M3 | `ui.settings.options.ask` dropped from `eventDataClasses` (ai.agent registration) | TestEventDataClass_AIAgentEmptySetAndOwnEvents, TestEventDataClassRegistry |
| M4 | cache key ignores values | TestSettingsOptions_Cache, TestSettingsOptionsCache |
| M5 | cache key ignores version | TestSettingsOptionsCache |
| M6 | no `dropPlugin` on save | TestSettingsOptions_Cache |
| M7 | malformed answer cached | TestSettingsOptions_FailuresShowNoticeAndAreNotCached/malformed |
| M7b | timeout/error/panic cached | …/timeout, …/error |
| M8 | save refuses an asked value | TestSettingsOptions_SaveRule (64 bytes → 422) |
| M9 | save accepts any asked value | TestSettingsOptions_SaveRule (65 bytes, comma → 200 and written) |
| M10 | secrets (by type and by name) sent in `values` | TestSettingsOptions_FragmentAsks… (payload leaks typed key), TestSettingsAskValues |
| M10b | only the name-heuristic secret check removed | TestSettingsAskValues |
| M11 | 33 options accepted | TestDecodeSettingsOptionsAnswer |
| M12 | 129-byte label accepted | TestDecodeSettingsOptionsAnswer |
| M13 | 16 KiB answer limit off by one / removed entirely | **SURVIVED** (false pass: the "too large" case padded with an unknown field `"pad"`, so `DisallowUnknownFields` refused it, not the size check). **Fixed the test**: valid JSON padded with whitespace to limit+1 (must fail) and exactly the limit (must pass). Now killed by both `>+1`, `if false`, and `>=` mutants |
| M14 | duplicate values accepted | TestDecodeSettingsOptionsAnswer |
| M15 | `boundedAskPlugin` without its own deadline | TestBoundedAskPlugin…/Failures |

Test change (only file touched): `internal/pages/plugin_settings_options_unit_test.go` (case
"too large" → "one byte over the size limit" + "at the size limit ok"). gofmt/vet clean.

## UX §7 gate (ux.md §5), item by item
| # | Item | Result | Evidence |
|---|---|---|---|
| 1 | Surfaces: kiosk (`UT_KIOSK=1`, body.kiosk) and windowed at 1024×600, desktop, 360 px en+fa; S2/S4/S5 shots looked at | **PASS** (emulated, Chromium) | kiosk: select 46 px high, no h-scroll, status bar visible at the bottom; the feature adds no overlay, so status/lock/exit reach is unchanged (lock is not on the rail in this build — not this card). Real till touchscreen not available |
| 2 | GET has stored value selected + empty `#ps-opts-msg-model[role=status]`; GET returns before the plugin answers | **PASS** | stored provider = `hang` (6 s): GET 96 ms; HTML has `retired-model selected` and the empty status div |
| 3 | S1: select enabled + `aria-busy`; "Loading choices…" after ~400 ms, never on a cached answer | **FAIL (D1)** | select enabled, `aria-busy="true"` at 150 ms, removed after. But the loading line is **opacity 1 from the first ms** (sampled every 40 ms from 6 ms) |
| 4 | Answer order, `<b>Big</b>` as text, `dir="auto"` | **PASS** | order `retired-model, alpha-small, alpha-big`, all `dir=auto`; option text `<b>Big</b> alpha`, 0 `<b>` elements |
| 5 | S3 empty hint without Try again; declined/no-hook → hint, not failure | **PASS** | provider d → "The plugin has no choices to offer yet…", 0 retry buttons; declining `extra_model` shows the same hint |
| 6 | S4 timeout/malformed: note + Try again within ~5 s, stored selected, Save works; retry re-asks; focus to select | **PASS** | hang: failure ≈5.2 s after the ask started, value kept; 33-option answer refused → note + Try again (46.2 px); Enter on Try again sent 1 new POST; focus then `#ps-f-model`; Save → "Settings saved" |
| 7 | S5 stored value missing stays selected+first, hint shows | **PASS** | value `retired-model`, first; saved-missing text present |
| 8 | Provider change (unsaved) refreshes once; text depends_on asks on blur only; picking the asked select asks nothing | **PASS** | +1 ask on provider change; 0 asks while typing 4 chars, +1 on Tab/blur; 0 asks after picking a model |
| 9 | A6: pick during an in-flight ask is kept | **PASS** | provider `slow`, picked `alpha-big` at 300 ms → value `alpha-big` after the answer; 2 asks (the re-ask) |
| 10 | S7 hidden field sends nothing until shown; after Save re-asked; 422 keeps the pick | **PASS** | `extra_model` 0 asks while hidden, 1 when `show_more=yes`; a re-ask follows each Save; tampered provider → 422, model pick `alpha-small` kept, focus to the bad field |
| 11 | Live region exists before text; failure/hints announced once, loading not; `aria-describedby` has msg id | **PASS on DOM / SR not run** | container is in the GET HTML; `aria-describedby="ps-opts-msg-model"`; loading line is not in a live region. No screen reader here. Risk: every re-ask rewrites the message's innerHTML even when the text is the same (for example a cache hit after a provider round trip), which some screen readers announce again |
| 12 | No focus change/OSK on load or answer; Tab order select → Try again → next | **PASS** | activeElement on load = BODY; Tab from select → Try again → `setting_show_more` |
| 13 | 360 px fa: no h-scroll with a 128-byte label; Try again ≥46 px; long text wraps | **PASS** | scrollWidth 360 = clientWidth with the 128×"W" option selected (the select clips the label at 285 px wide); retry 46.2 px; the note grown by 40 % still has no h-scroll. No `de` locale ships, so I used en +40 % |
| 14 | Touch drag-scroll over select and message; picker open during a swap | **PASS (emulated) / NOT VERIFIED** | CDP touch drag scrolled the page 0→172 px over the select and 0→174 px over the message. The native picker during a swap needs real Android/Pi touch hardware (headless Chromium draws no native picker) |
| 15 | No showModal / inline handlers / hx-on; logical CSS; 5 keys + meta; help mentions loading, empty, Try again | **PASS** | guards rc=0; the diff adds none; `en.json` and `en.meta.json` have all 5 keys; `plugins.md` covers all three |
| 16 | No-JS: stored + static options, saves | **PASS** | JS off: model shows `retired-model`; native POST saved provider=b, model kept |

## Visual attestation (deviceScaleFactor 1, `scratchpad/4160/shots/`)
- `s2-loaded-1024-en.png`: the saved asked pick `<b>Big</b> alpha` shows as text, no message line, labels sit above full-width fields. Clean.
- `s5-saved-missing-1024-en.png` and `s5-kiosk-1024x600-en.png`: `retired-model` selected; the hint wraps on two lines under the select, inside the card, with no overlap.
- `s4-refused-tryagain-1024-en.png`, `s4-timeout-1024-en.png` and `s4-kiosk-1024x600-en.png`: the failure note wraps under the select. "Try again" is a secondary button on its own line at inline-start. The next field starts below it with spacing, and nothing is clipped.
- `s3-empty-1024-en.png`: the empty hint wraps cleanly, with no button, and "Settings saved" sits beside Save.
- `s1-loading-at-1s-1024-en.png`: the muted "Loading choices…" line sits under the select. Placement is fine, but see D1 for the timing.
- `s2-longlabel-360-fa.png`: RTL page; the 128-byte "WWWW…" label is clipped inside the select and the card does not widen. The hint is the English fallback (no fa key yet, ADR-0160), so its final period renders at the line start (".pick another"). That is a bidi artefact of English inside RTL, and it goes away once the text is translated. Cosmetic only.
- `s4-failure-360-fa.png`: the note wraps in 3 lines and "Try again" sits at inline-start (right) at 46 px, inside the card.
- `nojs-before/after-save-1024-en.png`: plain form, saved.
- Full-page shots show the fixed rail and status bar mid-page. That is an artefact of the full-page capture, not of the layout.
- **Not looked at:** dark theme (the theme is a till setting; `emulateMedia` dark gave an identical render, so dark was not exercised), ar/tr, a real screen reader, real touch hardware, and WebKit.

## Defects
**D1 (production, UX §1 S1 / A1, §7 item 3; bounce to Dev): "Loading choices…" shows immediately, not after 400 ms.**
- `web/public/app.css:7682-7684` (`.ps-ask-loading { opacity: 0 }` + `.htmx-request .ps-ask-loading { animation: ps-ask-appear 0s linear .4s forwards }`) and `web/ui/pages/plugin_settings.html:313` (the span also has class `htmx-indicator`).
- Cause: htmx injects `.htmx-request .htmx-indicator{opacity:1; transition…}` (specificity 0,2,0), which beats `.ps-ask-loading{opacity:0}` (0,1,0). The span is therefore visible for the whole request from 0 ms, and the delayed animation changes nothing. The same happens for every cache hit, for the length of its round trip; on a Pi that is a visible flash.
- Repro: run a till with the tester guest, open `/plugins/com.example.e2e-settings/settings`, and pick a provider whose answer takes 1.5 s. `getComputedStyle(.ps-ask-loading).opacity` is "1" at 6 ms (`probe.js`). On fx-light **and** fx-full.
- **Fix caveat:** a CSS-only fix such as `.ps-ask.htmx-request .ps-ask-loading{opacity:0}` gives the right 400 ms delay on fx-full (verified: 0 until ~438 ms, then 1). But under `html.fx-light` (`app.css:517-519`, `animation: none !important`, which Pi tills auto-select) the line would then **never** appear. Use a JS timer (add a class 400 ms after `htmx:beforeRequest`, drop it on `afterRequest`) or another mechanism fx-light does not strip.
- The ut-docs `reference/plugin-views.md` line "a 'Loading choices…' line appears after 400 ms" is also untrue today.
- The e2e does not cover S1 at all. Add an assertion (opacity 0 at ~200 ms of a slow ask, 1 at ~600 ms) when it is fixed; this needs a slow provider in `settings_guest`.

**T1 (test gap, fixed here):** M13 above. The answer-size bound was unguarded.

**Observation (latent, not reproduced): scope matching for a key with both a global and a register row.**
- `plugin_settings_options.go` `registerSettingsOptions` picks `row == nil || rows[i].Scope == scope`. For the global wrapper `scope` is `""`, which never equals `"global"`, so the **first** row wins.
- `ListPluginSettings` orders only `BY key`. A register row sorted first would draw the global field from the till row and flip its `hx-post` to `?scope=register`. My repro test (deleted afterwards) got global first in SQLite's current plan, so this is not shown to break today.
- Suggest matching `scope == "" → "global"` explicitly.

## Housekeeping
- Tills on :9601/:9602 were stopped and the e2e tills stopped themselves.
- The scratch data dirs `till-*` are in the scratchpad.
- The guest was built with `-overlay`, so the repo is untouched.
- No test data went to any shared system.
