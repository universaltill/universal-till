# Review: App Store build — broken code plugin says it can't run, offers Uninstall (ut-docs#7017)

Pre-flight: till user-visible behaviour: `web/help/en/plugins.md` gains one sentence (broken code plugin on the iPhone/iPad app is not reinstalled; Uninstall it); `make docs-shots` re-run after CI's `guard-docs-shots_test.sh` flagged the stale record: only `web/help/img/manifest.json` changed (en/plugins topic hash, `plugins_page.go` and `plugins.html` surface entries); every PNG is byte-identical, since no docs-shot fixture renders an App Store broken code plugin; `reference/` n/a (no reference page describes the Plugins card chips; ADR-0194 already says code plugins are never fetched on that channel); `README.md` n/a; new `en.json` keys: 1 (`plugins.manage.broken_code_refused`), with `en.meta.json` metadata (standard, area plugins), English only; my./admin. n/a; host function / manifest field n/a; review record: this file; follow-ups: none.

Card: universaltill/ut-docs#7017 (complexity:easy), lane:cloud-41.
Author: Sonnet (Dev subagent). Reviewer: Opus (independent subagent, different model). Tester: separate Sonnet subagent.
BA/Architect/UX for this easy card were one orchestrator design note (`codeRefused` = install_state broken AND not bundled AND `plugins.CodeRuntimeRefused(runtime)`), not separate subagents — recorded here as a deviation.

## Change
- `internal/data/plugin_repo.go`: `ListManagedPlugins` also reads `COALESCE(runtime,'')` into `ManagedPluginRow.Runtime`.
- `internal/plugins/code_gate.go`: `CodeRuntimeRefused(runtime)` = `RefuseCodeRuntime(runtime) != nil` (one home for the channel rule; empty runtime fails closed).
- `internal/pages/plugins_page.go`: item flag `codeRefused`.
- `web/ui/pages/plugins.html`: two Broken chips with static `title` (normal broken → `plugins.status.broken_binary`, unchanged; code-refused → new key) and a visible `notice-block-warn` note on refused rows. Uninstall button unchanged (shown for every non-bundled row, UT confirm flow).

## Findings (round 1) and resolution
- **B1 blocker — fixed.** The first cut used `:title="p.codeRefused ? '{{ T a }}' : '{{ T b }}'"`. html/template only HTML-escapes a `:title` attribute, so the apostrophes in both English strings ("can't", "plugin's") became `'` again in the browser and the Alpine expression was a SyntaxError: every Broken chip on every channel lost its tooltip. The Tester reproduced it independently in Chromium with the real alpine.min.js (`Alpine Expression Error: Unexpected identifier 't'`). Fix: two spans with static `title`. Regression test `TestPluginsHTML_NoTranslatedStringInBoundAttr_7017` fails on the old form (checked by re-planting it) and passes now.
- **M1 — not a defect.** The reviewer saw `CodeRuntimeRefused` returning `false`; that was the Tester's temporary false-pass revert on the shared checkout. The Tester verified the restore byte-for-byte; I re-read the file before commit.
- **M2 — fixed.** The page test's note-markup/"Uninstall" assertions were always true (`x-show` markup is always in the HTML); removed. The JSON `codeRefused` flag is the asserted signal; visibility was checked in the driven run.
- **M3 — fixed.** Added an App Store + empty runtime case (fail closed → true) and a bundled case (AC5, with a control flipping trust so it can fail).
- m1 — fixed: the text says "Uninstall it…" to match the button. m2 — fixed: `"area": "plugins"`. m3 — accepted: a legacy broken row with an empty runtime shows the refused note on the App Store build, matching the fail-closed install gate.

## Verification
- TDD: `TestCodeRuntimeRefused_7017` and `TestPluginsPage_CodeRefusedFlag_7017` failed first (undefined symbol / missing flag), pass now. False-pass check by the Tester: forcing the flag false or dropping `!isBundled` fails the tests.
- Driven (Tester): /plugins rendered for the App Store channel with a broken wasm row and a broken runtime-none row, loaded in Chromium with the real assets: the note is visible text on the refused row, hidden on the other; Uninstall present on both.
- I re-ran after the fix round: `go test ./internal/pages -run 7017`, `go test ./internal/plugins -run CodeRuntimeRefused` pass; gofmt clean; `golangci-lint run ./internal/pages/... ./internal/plugins/ ./internal/data/` 0 issues. Fix round also ran `go test ./internal/pages -run 'Plugin|7017'`, `go test ./internal/plugins -run 'Code|Gate|Refus'`, guard-i18n, guard-i18n-meta, guard-rtl-logical-css, guard-help-topics — all pass. Full package suites and e2e run in CI.
