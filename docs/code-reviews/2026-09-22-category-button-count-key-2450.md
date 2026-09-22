# Code review: split sell-screen category button count into its own i18n key (ut-docs#2450)

**Date:** 2026-09-22
**Card:** universaltill/ut-docs#2450
**Author:** scrum-master pipeline (`lane:cloud-41`), on behalf of the pipeline owner
**Reviewer:** independent fresh-context Sonnet subagent, isolated worktree (`complexity:easy` → Sonnet review, per `scrum-master`'s model routing)

## What changed

Split out of the ut-docs#2307 review (finding L1). The sell-screen
category-overflow sheet (`web/ui/partials/buttons.html`) reused
`categories.item_count` — the admin catalog table's own real-item-count
key (`web/ui/pages/categories.html`) — for a different number, the count
of quick-buttons wired up in that category. Same locale string, two
different numbers depending on which screen you were on.

1. **`web/locales/{en,fa,tr,ar}.json`** — new key
   `products.category_button_count`, keeping the `%d` verb: `"%d
   button(s)"` (en), `"%d دکمه"` (fa), `"%d düğme"` (tr), `"%d زر"` (ar) —
   matching each locale's existing bare-noun/no-plural-marker convention
   already used by `categories.item_count` itself.
2. **`web/ui/partials/buttons.html`** — the overflow tile's
   `.category-overflow-count` span now reads `products.category_button_count`
   instead of `categories.item_count`. `web/ui/pages/categories.html` is
   untouched.
3. **`internal/config/i18n_test.go`** — new
   `TestRealLocaleBundle_CategoryButtonCountResolvesInEveryCoreLocale`,
   loading the REAL embedded `web/locales` bundle (`locales.FS`, not a
   synthetic fixture like every other test in this file) and asserting the
   key resolves with a `%d` verb in en/fa/tr/ar and differs from
   `categories.item_count`'s own value.

## Independent review (fresh-context Sonnet, isolated worktree)

**First pass verdict: FAIL (blocking).** The reviewer:

- Confirmed the diff scope (6 files, `categories.html` untouched) and that
  no secrets/real client names were introduced.
- Ran `go build ./...`, `go vet ./...`, `scripts/ci/guard-i18n.sh`,
  `go test ./internal/config/... ./internal/pages/...` — all passed.
- **Independently re-verified the TDD claim itself**: reverted only the new
  `en.json` line, re-ran `TestRealLocaleBundle_...` and confirmed it failed
  with the exact claimed bare-key-fallback message, then restored the line
  and confirmed it passed again.
- Checked the fa/tr/ar translations are genuine (right script/language,
  matching house style) rather than copy-pasted English.
- **Found a real, deterministic e2e regression** the required check
  commands couldn't see: `e2e/tests/sale-screen-category-strip-overflow-2307.spec.ts:181`
  asserted `.category-overflow-count` `toHaveText('1 item(s)')` — this fix
  changes that exact element's text to `"1 button(s)"`, so the spec would
  fail for all 15 seeded categories once the e2e suite ran.
- **Non-blocking**: flagged `web/help/en/sell.md`'s "how many items it has"
  wording (describing the same overflow-sheet tile) as now stale.

## Findings applied

- `e2e/tests/sale-screen-category-strip-overflow-2307.spec.ts` — updated the
  assertion to `'1 button(s)'` and the neighbouring comment. Re-ran the
  spec file directly (`npx playwright test tests/sale-screen-category-strip-overflow-2307.spec.ts`)
  — all 4 tests pass.
- `web/help/en/sell.md` — reworded to "how many quick buttons it has".
- `web/help/img/manifest.json` — regenerated via `make docs-shots` (124/124
  screenshots passed) since the topic markdown changed; no screenshot
  pixels actually changed (the overflow sheet isn't open in the captured
  `sell` screenshot for any locale), confirmed by `guard-docs-shots.sh`
  going green with an unchanged surface hash.

## Verification beyond the independent review

- `go build ./...`, `go vet ./...`, `scripts/ci/guard-i18n.sh`,
  `go test ./internal/config/... ./internal/pages/...` re-run clean after
  the fixes.
- `bash scripts/ci/guard-docs-shots.sh` — green.
- `npx playwright test tests/sale-screen-category-strip-overflow-2307.spec.ts`
  — 4/4 pass, including the corrected assertion.
- `scripts/ci/check-lang-pack-drift.sh` confirms `products.category_button_count`
  is a brand-new core key (not present on either pack's own `main`) — the
  documented "merge core first, pack catch-up follows in the same cycle"
  case, not a pack-drift oversight. Follow-up PRs against
  `ut-plugin-language-de`/`ut-plugin-language-es` are prepared and land
  once this PR merges.

Verdict: **SAFE TO MERGE** (after the two findings above were fixed).
