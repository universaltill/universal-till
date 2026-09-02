# 2026-09-02 — promotions/payments-fee write-path currency-decimals fix (ut-docs#1400)

## What shipped

`internal/pages/promotions_page.go`'s `parsePromotionForm` ("amount" case)
and `internal/pages/settings_page.go`'s `POST /api/settings/payments-fee`
handler both hardcoded `* 100` to convert an operator-entered major-unit
amount into minor units for storage, instead of using the shop's
configured currency's decimal exponent
(`httpx.ActiveCurrency().Decimals`). On a 0-decimal currency (IRR, IRT,
IQD, AFN, JPY — minor units ARE major units) this stored a
100x-too-large value: an operator entering "500" for ¥500 got 50000
minor units persisted. This is a write-side data-integrity bug, more
severe than ut-docs#1290's display-only prefill bug in the same two
forms (that one only showed a wrong number; this one persisted one).

Fix: added `httpx.MinorFromMajor(major float64, decimals int) int64` in
`internal/httpx/currency.go` — the write-side inverse of the pre-existing
`FormatMajorPlain(minor int64, decimals int) string` (ut-docs#1274),
same `pow = 10^decimals` computation and rounding rationale. Both call
sites now use it via `httpx.ActiveCurrency().Decimals`. Surgical scope:
validation logic at both sites is unchanged, and
`settings_page.go`'s `bp := int64(math.Round(pct * 100))` (a
basis-point percentage, not money — `internal/money`'s own rule is that
basis-point rates stay `int64`) is deliberately left untouched, now with
a comment explaining why.

## Independent review

Reviewed by a fresh-context Sonnet subagent (complexity:easy →
Sonnet-reviews-Sonnet per the model-routing rule), isolated in its own
git worktree so its TDD revert/restore never touched the shared
checkout. **Verdict: safe to merge as-is — no findings, no fixes
needed.**

What it verified, independently:

- Re-derived the diff scope from `git show`/`git diff --stat` itself
  (6 files, no `web/ui/**` or `web/help/**` touched — confirmed rather
  than assumed, so the UX-guidelines and help-manual requirements
  genuinely don't apply here).
- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.
- Full `internal/httpx` and `internal/pages` (+ subpackages) suites —
  green.
- **TDD red→green re-verified independently** (not taken on the
  implementer's word): reverted only the 3 production files to their
  pre-fix (`HEAD~1`) content inside its isolated worktree, left the
  tests at HEAD, and re-ran:
  - `TestMinorFromMajor` → compile failure (`undefined: MinorFromMajor`)
    — legitimate, the function genuinely didn't exist pre-fix.
  - `TestPromotionsPageCreate_AmountIsCurrencyDecimalsAware` →
    `value = 50000, want 500 minor units under a 0-decimal currency
    (not 50000)`.
  - `TestPaymentsSettingsFee_IsCurrencyDecimalsAware` →
    `stored fee under a 0-decimal currency = {BP:175 Fixed:50000}, want
    {BP:175 Fixed:500}`.

  All three failed with real, meaningful errors reproducing the exact
  bug — not masked/compile-only failures. Restored the 3 files
  (`git checkout HEAD -- <files>`), confirmed the tree byte-identical
  to HEAD, and re-ran to green.
- **False-pass risk in the two new page-level tests, checked
  specifically**: both use exact typed comparisons — a direct
  `int64` DB-column scan compared with `!=` (promotions) and a
  `json.Unmarshal` into a typed struct compared field-by-field
  (settings) — neither uses a substring match, so neither is
  vulnerable to `"fixed":500` silently matching inside the bug's own
  `"fixed":50000"` output. (An earlier draft of the settings test did
  use `strings.Contains` and was caught and replaced during this same
  session's own TDD revert check, before review.)
- Fix-logic scrutiny: `MinorFromMajor`'s `pow` loop correctly yields 1
  for `decimals<=0`; rounding behavior matches the pre-existing `*100`
  literal's for 2-decimal currencies (no regression on the common
  case); `httpx.ActiveCurrency()` is the same accessor used
  consistently elsewhere in the codebase for decimals-aware money
  handling (`shifts_page.go`, `invoice_page.go`, `import_page.go`,
  `ask_api.go`); validation logic at both call sites is byte-identical
  to before the fix.
- `scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` —
  both pass.
- No file-write handlers in this diff (no `os.MkdirAll`/`paths.Data`
  concern); no demo/client data; no secret-shaped literals.

## Verified beyond automated tests

No UI/template/JS surface is touched by this change (server-side parse
logic only), so there is no visual surface to screenshot or drive
through a browser — confirmed from the diff itself, not assumed.

## Explicitly deferred / out of scope

- `settings_page.go`'s `bp` (basis-point percentage) conversion —
  correctly out of scope, not money.
- ut-docs#1401 (shifts_api.go's `respondCloseSuccess` hardcoded
  `£%.2f`/`/100`) — a separate, already-filed follow-up in the same
  defect family, not touched here.
- ut-docs#1290 (PR #718, in review under a different pipeline lane at
  the time of this change) — the *display/prefill* side of these same
  two forms; no overlap with this write-path fix.

## Safe-to-merge verdict

Yes. Independent review found nothing blocking; full gate (build, vet,
gofmt, tests, guards) green; TDD claim independently re-verified
red→green in an isolated worktree.
