# Code review: admin-review UI batch, merged tree (batch/ar-ui-2026-11-19)

**Date:** 2026-11-19
**Cards:** ut-docs#6148 #5334 #5607 #5898 #5894 #5773 #5877 #3468 #3109 #1387 #5989
**Reviewer:** claude-opus-5-5 (devs were Sonnet), one combined Reviewer over `git diff origin/main...HEAD`
at 3975e26b3. Builds on the per-branch Fable pass (`temp/review-ar-batch.md`, one record per card in
this folder) and the Tester pass (`temp/ar/tester-ui.md`, its two e2e failures fixed in 3975e26b3).
This pass looked at what the merges could break.

## What shipped
The eleven card branches merged into one batch branch, plus main. Per-card scope is in each card's
own 2026-11-19 record.

## Merge-integration checks
- `internal/pages/shift_close_slip_print.go` (5877 + 5773): `buildShiftSlipDoc(slip, shopNameOrEmpty(ctx, d),
  cfg.Charset, reprint)`, both sides kept. No `storeNameOrDefault` caller left. The reprint hidden field,
  the row target from the DB id, and the audit `reprint` flag are all present.
- `internal/pages/setup_page.go` / `setup_cloud_pair.go` / `sync_api.go` (5894 + 5898): both error keys
  are in the step-4 list and the echo guards. `completeJoin` and pair-start check the length before they dial.
- `web/locales/en.json` / `web/i18n-meta/en.meta.json`: valid JSON, no duplicate key in either, every
  new en key (11) has a meta entry, no key removed, no meta entry without an en key. No non-English
  locale or translated help file is touched (ADR-0160). `guard-i18n`, `guard-i18n-meta` green.
- `web/public/app.css` (5877, 3109, 5334, 1387, 5607, 5989): braces balance (2275/2275), the 5334
  `@media`/`@supports` blocks close correctly, logical properties only (`guard-rtl-logical-css` green).
- `web/locales/en.json-E` (a sed artefact on main) is deleted. Nothing references it.
- Non-English text in code: only e2e-injected candidate strings and Go test fixtures. No product
  string is hard-coded.
- CLAUDE.md: the new `UT.confirm` verb rule is in place, and its guard test
  `TestUTConfirm_EveryCallerNamesItsVerb` passes.

## Findings
| # | Finding | Severity | Result |
|---|---|---|---|
| 1 | `guard-docs-shots.sh` red on the merged tree. The surface hash is stale, and so are the topic hashes for 8 en topics (display, kitchen-stations, multitill, open-orders, reports, self-order, sell, users). Each branch refreshed its own manifest and the merges kept only one. After merge, main's push run would turn red. | medium | fixed: topic hashes recomputed from the merged markdown, surface hash via `update-docs-shots-surface-hash.sh`, guard green. PNGs not re-rendered (see Deferred) |
| 2 | Follow-up F1 of #5894: the cloud `set_setting` path still writes `store.name` unchecked by design, and no card existed (Fable pass item) | low | filed ut-docs#6747 (Backlog) |
| 3 | 5877: a crafted POST without `reprint=1` prints an old slip with no "Reprinted" line (still PIN-gated and audited) | low | accepted, as in the card record |
| 4 | 3468: the hint promised full screen on macOS / without the desktop app (Fable pass item) | low | already fixed in 760e20f54. The text now says the window stays normal there |
| 5 | 3109 / 3468: translations in a code PR (Fable pass item) | medium | already fixed in 9fb4181ad / 760e20f54. Only en files are left in the diff |

No blocker. Money: the only print-path change is the 5877 reprint line and the 5773 blank-name row skip.
No amount, rounding or audit semantics changed.

## Verification
- `go build ./...` clean; `go vet` clean on pages, print, config, settings, discovery, ui. `go test` on
  those six packages green on this tree.
- TDD re-verified by reverting each fix:
  - Reverting `internal/print/escpos.go` to main fails `TestRender_NoStoreNameRow` and
    `TestRenderText_NoStoreNameRow`; with the fix restored they pass.
  - Removing the reprint `Meta` line in `buildShiftSlipDoc` fails the `ShiftCloseSlip` tests; with the
    line restored they pass.
- Guards green: `guard-i18n`, `guard-i18n-meta`, `guard-rtl-logical-css`, `guard-data-access`,
  `guard-help-topics`, `guard-help-drift`, `guard-kiosk-engine`, `guard-wip-commits`, and `guard-docs-shots`
  after fix 1.
- e2e: no new run. The Tester's two failures were test-only and fixed in 3975e26b3. That commit touches
  only two spec files.

## Verdict
PASS. Ready to merge once CI is green.

## Deferred items
- Docs-shots PNGs for the surfaces this batch changes are not re-rendered: shifts History row,
  kitchen-stations joined hint, Settings → Display hint, open-orders takeaway chip. `make docs-shots`
  cannot run on macOS (font pin, ut-docs#4407), and no Docker daemon is up on this host. The manifest is
  accurate for the guard, so the screenshots refresh on the next `make docs-shots` run in the Linux
  container.
- ut-docs#6747 (5894 F1).

Pre-flight: help topic `updated` (en only: display, kitchen-stations, multitill, open-orders, printing,
reports, self-order, sell, users); docs-shots `manifest hashes refreshed, PNGs deferred (finding 1)`;
en.json keys `en.meta.json updated` (11 new keys, English only, ADR-0160); README / website `n/a (no plan,
price or platform claim)`; ADR `n/a`; review record `this file`; follow-up cards `ut-docs#6747`.
