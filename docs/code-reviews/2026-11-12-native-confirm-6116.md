# Code review: native window.confirm → UT.confirm, guard-no-native-confirm (ut-docs#6116)

- Repos: universal-till `feat/6116-native-confirm` @ 3ae079689 (2 commits on top of origin/main 867a369e1); ut-docs `docs/6116-native-confirm` @ 4e8d7d27.
- Reviewer: independent subagent, Opus 5.5 (the code was written by Sonnet). It ran in an isolated detached worktree.
- Date: 2026-11-12

## What shipped
- `web/public/record-dialog.js`: the discard guard is now asynchronous. `isDirty()` and `askDiscard()` replace `confirmDiscard()`. A clean form still closes or reopens synchronously. A dirty form asks through `UT.confirm` with an opener. The `data-record-discard-asking` attribute single-flights Close, Escape and New while the question is open. The code acts only on `ok === true` and re-checks that the dialog and row are still connected and the dialog is still open. On Cancel, `keepFocus()` keeps focus inside the dialog.
- `plugins_store_detail.html`: the ADR-0006 trust consent for an unverified publisher goes through `UT.confirm` and fails closed: only `true` with the button and card still connected sends. The request is factored into `send()`. `$&` in a vendor name is neutralised with a function replacer.
- `plugins.html`: Uninstall and Roll back go through `window.actAfterConfirm`.
- `catalog.html`: `utCatalogGoToModifiers` asks asynchronously and always returns `false`, so inline-actions keeps preventing the default.
- `bugreport_panel.html`: Discard asks asynchronously. After it, focus goes to the first visible opener (#bugreport-toggle, or ☰ at phone width).
- `app.js`, `inline-actions.js` and `till-confirm.js`: the existing native fallbacks now carry `native-confirm:allow <reason>`.
- New `scripts/ci/guard-no-native-confirm.sh` and `_test.sh` (mktemp fixtures), both wired into the ci.yml `build` job. CLAUDE.md has a new Offline-first bullet.
- Specs: new `native-confirm-6116.spec.ts`. Ten existing specs now answer `#ut-confirm` instead of `page.on('dialog')`.
- ut-docs: `reference/coding-standards.md` §10 paragraph and `reference/list-and-dialog-pattern.md` discard-guard bullets.

## Findings
| # | Severity | Where | Finding | Status |
|---|---|---|---|---|
| F1 | **Medium (blocking)** | `scripts/ci/guard-no-native-confirm.sh:61-75` (`strip()`) | **False negatives on the real tree.** `strip()` opens a block-comment state at any `/*`, including one inside a `//` line comment or a string/attribute. `//` is only removed afterwards (line 83), so the state is already open. The guard then skips every line up to the next `*/`. Measured on HEAD: `web/public/app.js:2355` (`// … /api/buttons/* …`) hides **567 lines** (to 2922). `web/ui/pages/self_order_shop.html:108` and `web/ui/pages/bluetooth_devices.html:168` (`// … /api/… /*`) hide **the rest of each file**. `catalog.html:533` (`accept="image/*"`) hides 6 lines. Repro: insert `if (confirm('planted')) go();` on the line after any of those four. The guard passes (rc=0) in all four cases; I checked this and restored the files. Known gap (1) in the header covers only strings, not `//` comments, and it understates the scale. | **Fixed in this branch.** In `strip()`, treat `//` at the line start or after whitespace as end-of-code when it comes before the first `/*` or `<!--`. Also open a block comment only when `/*` is at the line start, after whitespace or after `{{`/`{{-` (a missed strip is a safe false positive). Add `_test.sh` cases: `// see /api/*` followed by `confirm('x')` on the next line must fail, and `accept="image/*"` followed by `confirm(` must fail. |
| F2 | Low / info | guard scope | `internal/recovery/templates/{recovery,safemode}.html` are till-rendered HTML outside `web/ui`. They have no `confirm` today and no `#ut-confirm` shell. | Accepted. Optionally name them in the header's known-gaps list. |
| F3 | Info | `record-dialog.js` async refactor | No defect found. Double-fire: ASKING plus till-confirm's one pending question. A detached dialog or row is re-checked after the answer. Escape inside `#ut-confirm` stops propagation and the record dialog's Escape is bound on the dialog itself. The promise always resolves (till-confirm's close→settle(false)) and never rejects, so ASKING cannot stick. No caller relied on a synchronous boolean: `close` and `open` are module-private, and `utCatalogGoToModifiers` still returns `false` synchronously. The focus trap leaves `#ut-confirm` alone (the `dialog[open]` top check). | none |
| F4 | Info | store trust consent | Fails closed on Cancel, Escape, swap, Lock and a pending question (`ok !== true`). If terms are unticked during the question, the download is sent without `accept_terms` and the server refuses it with `terms_required`. That is acceptable. | none |

## F1 fix — re-verified by the orchestrator
- `strip()` now uses `blockopen()`: `/*` opens a block comment only at the line start, after whitespace or after `{{`. It also uses `linecomment()`: `//` at the line start or after whitespace ends the code before any later `/*` or `<!--`.
- New `_test.sh` cases cover `// fetch /api/*`, `accept="image/*"` and a trailing `// … /*`, each followed by `confirm('x')`. The real `/* confirm */` and `{{/* confirm( */}}` cases still pass.
- The new test with the old guard (HEAD's) fails 3 cases (exit 1). With the fixed guard all pass (exit 0).
- Planting `confirm("planted")` after `catalog.html:533` makes the guard exit 1. The file was restored, and the real tree passes (exit 0). The Dev planted the same after the other three spots: all red, and no hidden native confirm was exposed.
- `shellcheck scripts/ci/guard-no-native-confirm*.sh` reports 0 issues (shellcheck-py).
- `guard-no-showmodal.sh` has the same flaw. It is filed as ut-docs#6132, not fixed here.

## Verification (run by reviewer, worktree at 3ae079689)
- `bash scripts/ci/guard-no-native-confirm.sh` → ✓. `bash scripts/ci/guard-no-native-confirm_test.sh` → all passed.
- `guard-no-showmodal.sh` + `_test.sh`, `guard-i18n.sh`, `guard-rtl-logical-css.sh`, `guard-ci-tests-sandboxed.sh`, `guard-fixed-dates.sh` → all ✓.
- `go build ./...` → ok.
- `shellcheck scripts/ci/*.sh`: **NOT RUN.** shellcheck is not installed in this container and the install was refused. The CI build job runs it.
- `cd e2e && npm ci && npx playwright test` with the 11 touched specs (native-confirm-6116, 2187, 3605, 3661, 2010, 5575, 2284, 5553, 2379, 2342, till-confirm-5524), `--project=default --workers=4 --retries=0 --trace off` → **114 passed**.
- `… catalog-store-detail-actions-4228.spec.ts --project=diagnostics --retries=0 --trace off` → **6 passed**.
- TDD re-verified: I reverted `record-dialog.js` and `plugins_store_detail.html` to origin/main.
  - native-confirm-6116 + categories-record-dialog-2010 → **8 failed** (6116 b, b2, c/g, e, f, AC12; 2010 c, c2), all on `#ut-confirm` not visible.
  - 4228 diagnostics → "unverified publisher: Cancel sends nothing, Confirm sends the POST" **failed**.
  - After restoring (`git status` clean) → 31 passed, then 6 passed.
- Guard false-negative probe (F1): four plants, all MISSED; files restored.
- ut-docs diff read (2 files, +17/−4): accurate, and it matches the shipped behaviour.

Pre-flight: till user-visible behaviour → `web/help/` n/a (no help topic describes the browser's confirm box; the confirm wording and flow are unchanged), docs-shots n/a (no help screenshot shows a discard or trust confirm), `reference/` updated (coding-standards §10, list-and-dialog-pattern), README n/a (not stale); `en.json` keys n/a (none added); CLAUDE.md updated; review record → this file; follow-ups → #6131 (deferred), #6132 (showModal guard strip() flaw), F1 fix in this branch.

## Verdict
**APPROVE after the F1 fix** (round 1 was REQUEST CHANGES for F1 only). The guard's comment stripper silently leaves about 600+ lines of the real till code unscanned, including the self-order page. The fix is small and local to `strip()` plus two test cases. Everything else is approved as is.

## Deferred
- ut-docs#6131: at ≤480px the discard confirm covers the record dialog's own Lock and status row (Tester D1). The cause is the app.css:3123 `#ut-confirm` phone placement. This is not a regression against main, where the native confirm made Lock inert too.
- ut-docs#6132: guard-no-showmodal.sh has the same `strip()` flaw as F1.
- F2 (recovery/safemode templates outside guard scope): informational, no card needed unless a confirm appears there.
- `alert()`/`prompt()` are not covered by the guard (known gap 5, as coding-standards §10 now states). Card it if wanted.
