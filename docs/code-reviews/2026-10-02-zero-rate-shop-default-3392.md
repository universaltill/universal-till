# Code review — a 0% shop default tax rate is charged 0% (ut-docs#3392)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3392 (sibling of ut-docs#3250, which fixed the same
  "0 means unset" bug class one layer down, for a line's own tax code).
- **Branch:** `fix/3392-zero-rate-shop-default`
- **Author:** Opus (this cycle's build model).
- **Reviewer:** independent pass, Fable (a different model from the author,
  per `MODEL-ROUTING.md`), no visibility into the implementation reasoning.
- **Verdict: SAFE TO MERGE**, after adding one WHY comment (finding 1).

## The bug

`internal/pos/service.go`'s `effectiveTaxRateBPFor` — the pure core every
basket total runs through — resolved a line with no tax code to the shop's
default rate and then, if that default was `0`, overwrote it with `2000`
("default to 20% if unconfigured"). A shop whose configured default rate is
genuinely 0% (the `US` and `OTHER` presets in
`internal/data/country_settings_repo.go`, or any shop that typed `0`) was
therefore charged 20% on every line without a tax code, in the live basket
and on the tender path alike. #3250 fixed the same shape for `l.TaxRateBP`
(a 0% tax code) but left this second fallback in place.

## What shipped

- `internal/pos/service.go`: the two-line `if standard == 0 { standard =
  2000 }` fallback is deleted. Nothing else in the function changes: the
  asker-first / tax-code / shop-default order is intact, and `blocked`
  (ut-docs#368 fail-closed) is still propagated.
- `internal/pos/zero_rate_shop_default_test.go` (new, 3 tests):
  `TestEffectiveLineTaxRateBP_ZeroShopDefaultChargesZero` (0% default, no
  tax code → 0; also with a healthy declining asker),
  `TestEffectiveLineTaxRateBP_StandardShopDefaultUnchanged` (20% default →
  2000, the regression guard), `TestBasketTotals_ZeroShopDefaultAddsNoTax`
  (`Scan` at a 0% default carries zero tax and `Total == 250` for both
  tax-inclusive and exclusive).
- `CHANGELOG.md`: one entry under Unreleased → Fixed, directly above its
  #3250 sibling, with an explicit **Behaviour change** sentence.
- `internal/pos/zero_rate_tax_code_test.go` (#3250's coverage) is
  byte-identical to `origin/main` — confirmed with `git diff origin/main`.

## Review findings

1. **LOW (fixed): no comment said why the fallback is gone.** This is the
   second time this exact bug class has been fixed in this function, and a
   future engineer reading `standard = defaultRateBP` with nothing after it
   could reasonably "fix" a perceived missing default and reintroduce the
   20% charge. Added a short WHY comment (ut-docs#3392) after the
   shop-default assignment stating that the default reaching the function is
   always a real value and that 0 means the shop chose 0%, with the two
   places that guarantee it. Comment only — no code change; the 3 tests
   were re-run green afterwards.

No other findings. Specifically re-traced, not taken from the BA/Tester
summaries:

- **Every non-test `Config.TaxRateBasisPoints` source is a real value.**
  `internal/pages/init.go` (cashier engine, kiosk engine, and the table-QR
  session factory which reads `kioskEngine.Config()`) and
  `internal/pages/engine_config.go` (`engineConfigFor`, applied on every
  settings save) all take `RuntimeState.TaxRateBP`. `common.LoadStateChecked`
  seeds that from `cfg.Locales.TaxRateBP` and only overwrites it when
  `taxrate.ParsePercent(store.tax_rate)` returns `ok`. `config.Init` seeds
  `cfg.Locales.TaxRateBP` from `ParsePercent(UT_TAX_RATE or "20")` and
  falls back to 2000 on `!ok`; `settings/runtime.go` has the same
  `ok`-guard. So "unset" never reaches the engine as 0 — only a parsed 0 does.
- **`taxrate.ParsePercent` cannot yield a false 0.** `""` fails the
  `whole == ""` check and returns `(0, false)`; `"0"` returns `(0, true)`.
  Non-digits, signs, `>2` fraction digits and `>100%` are all `!ok`.
- **Every writer guards on `ok`:** `setup_page.go` (`tax_rate_pct`),
  `settings_page.go` (`taxRatePct` form and the `/api/settings/upsert`
  `KeyTaxRate` case), `common.LoadStateChecked`, `settings/runtime.go`.
- **Zero-value `pos.Config{}` in non-test code:** repo-wide grep of
  `pos.Config{` and `NewServiceWithResolver(` finds exactly one —
  `internal/pages/open_orders_counter.go:131`, `var cfg pos.Config` taken
  only when `d.Engine == nil`. `init.go:354` always wires `Engine` in
  production, so that branch is a test fixture path, not a shop. The
  `internal/pages` suite (which exercises `legacyCounterOrderHeldSale`)
  passes with the fix, so no test silently depended on the 20% default
  there either. `scripts/smoke-offline-sale/main.go` sets 2000 explicitly.
- **No other hard-coded 20% fallback** remains in `internal/pos` (only the
  `// e.g. 2000 = 20.00%` doc comment on the field).
- **Over/undercharge:** a shop with a non-zero default is unaffected (the
  deleted branch was unreachable for it); a line on a tax code is unaffected
  (#3250's early return precedes the deleted code). Only a 0% default with a
  no-tax-code line changes, from 20% to the configured 0% — which is the
  fix. Completed sales store their own `TaxRateBasisPoints` and are untouched.
- No file I/O, no paths, no SQL, no locale keys, no UI surface, no
  real shop/client names in the test data.

## Verified beyond the automated tests

- **TDD claim re-verified independently by the reviewer**, inline in one
  step (revert → run → restore, tree never left mid-revert): with
  `if standard == 0 { standard = 2000 }` hand-restored,
  `TestEffectiveLineTaxRateBP_ZeroShopDefaultChargesZero` fails with
  `got (2000, blocked=false), want (0, false)` and
  `TestBasketTotals_ZeroShopDefaultAddsNoTax` fails with `a line at a 0%
  shop default must carry no tax, got 0.42` (250 × 2000⁄12000, the exact
  reported symptom); `StandardShopDefaultUnchanged` passes both ways, as a
  regression guard should. Fix restored: all three green.
- Regression gate, run by the reviewer on the final tree (with the comment
  from finding 1): `gofmt -l` on both changed Go files (clean),
  `go build ./...`, `go vet ./...`, `go test ./internal/pos/... -race
  -count=1`, `go test ./internal/pages/... -count=1`, `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-core-neutral.sh`,
  all green. (`scripts/ci/` has no tax- or money-specific guard; the
  kiosk-engine guard is the closest, since the kiosk engine takes the same
  `Config`.) `guard-deadcode-baseline.sh` reported two "new unreachable"
  functions, `internal/logging/file.go` `Stderr` and
  `timestampWriter.Write`, after warning that it skipped the
  `./cmd/unitill-desktop` root (no GTK3/WebKit2GTK headers in this sandbox).
  Every caller of `logging.Stderr()` is under `cmd/unitill-desktop/` and
  `timestampWriter.Write` is reachable only through it, and this diff does
  not touch `internal/logging` — a sandbox-only artifact; real CI analyzes
  all three roots and is the authority, checked at the DevOps step.
- No shop-owner-visible UI or help topic changes: `web/help/*/country-settings.md`
  already documents each country's default tax rate (0% for the US preset)
  as the rate the shop runs on; the till now does what the manual says, so
  there is no prose to update.

## Deferred / not a finding

- Nothing deferred. No new Backlog cards.
