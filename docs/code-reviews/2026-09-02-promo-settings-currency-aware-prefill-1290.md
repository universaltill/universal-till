# Code review — promo/fee edit-form prefills weren't currency-aware (ut-docs#1290)

- **Date:** 2026-09-02
- **Branch:** `fix/1290-promo-settings-currency-aware-prefill`
- **Reviewer:** independent reviewer (fresh-context, different instance —
  Sonnet, this pipeline's `complexity:easy` review tier)
- **Verdict: SAFE TO MERGE. No blocking findings.**

## What shipped

Same root defect as ut-docs#1274's `CarryForwardDisplay` (found while
scoping that card): a Go-side minor→major prefill string hardcoding a
2-decimal `fmt.Sprintf("%.2f", float64(x)/100)` conversion instead of
being `currency.Decimals`-aware, on two more edit-form prefills:

- `internal/pages/promotions_page.go`'s `newPromotionView` —
  `ValueAmountMajor` (the amount-type promotion edit input's prefill).
- `internal/pages/settings_page.go`'s `registerSettings` fee-row builder —
  `FixedMaj` (the payments-fee "fixed" input's prefill).

On a 0-decimal-currency shop (IRR/IRT/IQD/AFN/JPY) both silently rendered
a wrong prefill (e.g. ¥500 as "5.00" instead of "500"). Both now call
`httpx.FormatMajorPlain(minor, httpx.ActiveCurrency().Decimals)` — the
shared helper ut-docs#1274/PR #632 already added for exactly this case —
rather than writing a third copy. `PercentMaj` (a basis-point percentage,
not money) and the `pattern`/`placeholder` HTML attributes (already
currency-aware via existing `{{ if eq currency.Decimals 0 }}` ternaries in
`promotions.html`/`settings.html`) were correctly left untouched.

Adds 2 regression tests, one per prefill, each asserting the correct
2-decimal value under GBP, the correct 0-decimal value under IRT, and
explicitly that no stale 2-decimal value leaks once currency is 0-decimal.

## Review findings

None blocking. The independent reviewer verified:
- `go build ./...`, `go vet ./...` clean; `gofmt -l` on the four changed
  files empty.
- Both new tests pass; full `internal/pages/...` suite green (153s, no
  regressions).
- `FormatMajorPlain`'s negative/0-decimal/N-decimal handling and both call
  sites' argument order are correct.
- **TDD claim independently re-verified**: reverting only the two
  production-code hunks (not the tests) makes both new tests fail exactly
  at the 0-decimal assertion; restoring returns both to green.
- The claimed "write-path is a separate, out-of-scope bug" is real and
  checks out: `promotions_page.go:87`'s `major * 100` (form parsing) and
  `settings_page.go:541`'s `fixedMaj * 100` (payments-fee POST) are
  genuinely untouched by this diff and remain open defects.
- UX-guidelines checklist doesn't apply (Go-side formatting only, no new
  template/CSS/interactive surface) — noted rather than silently skipped.
  No `web/help/` change needed (display bug, not documented behavior).

**Non-blocking notes, filed as follow-up cards (not fixed here, per BA
non-goals — same convention #1274's own PR used):**
- ut-docs#1290 already named the two write-path bugs above; left open,
  unify with a fresh card below.
- New: `internal/pages/shifts_api.go:674,682`'s shift-close success
  message hardcodes `£%.2f` against `/100` (Expected/Actual/Variance/
  Skim/New-float) — same defect class plus a hardcoded currency symbol,
  found by the reviewer's broader `%.2f` sweep. Not an editable prefill
  (a confirmation message), so lower severity than the two write-path
  bugs, but real.

## Verification

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l` (4 changed files) | pass / pass / empty |
| 2 new tests | 2/2 pass |
| `go test ./internal/pages/...` (full package) | pass, 153s |
| `guard-data-access.sh` / `guard-i18n.sh` | pass / pass |
| TDD red→green, independently re-verified by the reviewer | reverting the two production hunks fails both new tests at the 0-decimal assertion; restoring returns both to green |

## Follow-ups filed (out of scope here)

- ut-docs#1290's own write-path bugs (`promotions_page.go:87`,
  `settings_page.go:541`) — filed as a new card.
- `shifts_api.go:674,682`'s hardcoded `£%.2f`/`/100` shift-close message —
  filed as a new card.
