# Sweep remaining hand-rolled currency.Decimals ternaries onto moneypattern/moneyplaceholder (ut-docs#1530)

**Date:** 2026-09-08
**Card:** ut-docs#1530 (`complexity:easy`, `p3`)
**PR:** universal-till#929

## What shipped

Four remaining call sites hand-rolled the same
`{{ if eq currency.Decimals 0 }}[0-9]+{{ else }}[0-9]+(\.[0-9]{1,2})?{{ end }}`
(or the placeholder equivalent) ternary instead of using the shared
`{{ moneypattern currency.Decimals <signed> }}` / `{{ moneyplaceholder
currency.Decimals <example> }}` template helpers (`internal/httpx/currency.go`)
introduced by ut-docs#1274:

- `web/ui/pages/menu.html` — `#pfand-amount` (pattern + placeholder)
- `web/ui/pages/promotions.html` — `value_amount` (dialog + inline row, pattern only)
- `web/ui/pages/settings.html` — `fixed` payments-fee field (pattern only)

All four switched to `moneypattern currency.Decimals false` (none of these
fields ever allowed a leading `-`, matching every old ternary) with
`menu.html` also switching its placeholder to `moneyplaceholder
currency.Decimals 0` (its only placeholder ternary; the other sites' placeholders
were either a static string or an i18n label, correctly left untouched).

## Why low risk / pure debt-reduction

Every currency this product ships today (`internal/httpx/currency.go`) is 0 or
2 decimals, so the hand-rolled ternary and the shared helper already produce
byte-identical output. This only matters if/when a 3-decimal currency
(KWD/BHD/OMR) ships — the four sites would otherwise need updating
individually instead of picking it up for free.

## Testing

- TDD: `internal/pages/moneypattern_sweep_test.go` (new) renders each of the
  3 touched pages via their real route + test-deps helper, extracts the exact
  `pattern=`/`placeholder=` attribute strings, and asserts byte-identical
  output for both a 0-decimal (IRT) and 2-decimal (GBP) currency, before and
  after the substitution.
- Mutation-tested by the independent reviewer: flipped one site's `signed`
  arg (`false`→`true`), confirmed the test fails with the wrong pattern
  (`-?[0-9]+...`), reverted, confirmed green again — proves the test is a
  real regression guard, not a rubber stamp.
- Full gate: `gofmt -l`, `go build ./...`, `go vet ./internal/pages/...
  ./internal/httpx/...`, `go test ./internal/pages/... ./internal/httpx/...`,
  `golangci-lint run ./internal/pages/... ./internal/httpx/...`,
  `bash scripts/ci/guard-i18n.sh` — all clean, run independently by Dev and
  Reviewer.
- `make docs-shots` regenerated for real (via the repo's pre-installed-Chromium
  fallback, `e2e/scripts/resolve-chromium.sh` — no network install needed).
  104 Playwright docs-shots tests passed. `guard-docs-shots.sh` green.
  `web/help/img/en/sell.png` shows a 7-pixel, max-channel-diff-of-1 delta —
  confirmed as font anti-aliasing render jitter (pixel-diffed against the
  pre-change PNG), not a structural change: an unrelated page
  (`ar/till-designer.png`, never touched by this diff) produced the same
  class of jitter on a completely independent regen, confirming it's
  pipeline noise rather than something caused by this change.
- `e2e/tests/osk-decimal-admin-fields-1275.spec.ts` run for real (same
  pre-installed-Chromium path): 11/11 passed, including the two sites this
  diff touches (`promotions.html value_amount`, `settings.html Payments fee
  fixed`) driven via real on-screen-keyboard keystrokes.
- No visible copy/i18n changed — only `pattern=`/`placeholder=` attributes,
  confirmed by direct diff read. No locale files touched, so no help-manual
  follow-up needed (CLAUDE.md's "manual ships with the feature" rule).

## Independent review verdict

**SAFE TO MERGE.** No findings. Reviewed by a fresh-context Sonnet subagent
(per this card's `complexity:easy` routing) that independently traced
`MoneyPatternAttr`/`MoneyPlaceholderAttr`'s rendering logic, mutation-tested
the new test itself, ran the full gate plus the real Playwright docs-shots
regen and the relevant e2e spec, and pixel-diffed the one changed screenshot.

## Deferred / out of scope

None — the card's three acceptance criteria are fully satisfied.
