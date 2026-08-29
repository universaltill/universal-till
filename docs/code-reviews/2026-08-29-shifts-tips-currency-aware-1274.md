# Code review — /shifts and the tips-entry field weren't currency-aware end-to-end (ut-docs#1274)

- **Date:** 2026-08-29
- **Branch:** `fix/1274-shifts-tips-currency-aware`
- **Reviewer:** independent reviewer (fresh-context, different model — Opus,
  this pipeline's `complexity:medium` review tier)
- **Verdict: found one blocking issue and several real hardening gaps in the
  first-pass diff — all fixed in this branch before merge, re-verified,
  full gate green.**

## What shipped

`/shifts` hardcoded `(£)` in four field labels (`shifts.counted_cash`,
`shifts.skim`, `shifts.amount`, `shifts.opening_cash`) and a fixed
2-decimal `pattern`/`placeholder` on five money inputs, and
`shifts_page.go`'s `CarryForwardDisplay` hardcoded `%d.%02d` against
`/100` — all wrong on a 0-decimal currency (IRR/IRT/IQD/AFN/JPY: 500 minor
units rendered "5.00" instead of "500", and the field's own `pattern`
rejected the correct value). `reports_tab_tips.html`'s `#tips-amount` had
the same pattern/placeholder defect (its label never had a hardcoded
symbol to begin with).

- **`internal/httpx/currency.go`** — `FormatMajorPlain(minor, decimals)`:
  a plain decimal major-unit string (no symbol, no grouping, no locale
  digits) for prefilling an editable decimal-mode input that
  `window.utCurrency.toMinor()` parses back.
- **`internal/pages/shifts_page.go`** — `CarryForwardDisplay` now calls
  `FormatMajorPlain(carryMinor, httpx.ActiveCurrency().Decimals)`.
- **`web/ui/pages/shifts.html`, `web/ui/partials/reports_tab_tips.html`** —
  labels use `{{ currency.Display }}`; pattern/placeholder are now
  currency-decimals-aware. `#adjust-pounds` keeps its `-?` prefix and
  negative example placeholder.

## Review findings, and what changed as a result

The independent reviewer (Opus) found six real issues; all six were
addressed on this same branch before merge (no finding was accepted as
"won't fix" — the "not in scope" items below were pre-existing, unrelated
to this diff, and are separately tracked, not waved off).

1. **BLOCKING — the user manual actively told the operator to type a value
   the new `pattern` would reject.** All four locales'
   `web/help/{en,fa,tr,ar}/reports.md` gave `"-50.00"` as the example
   negative-adjustment amount; under a 0-decimal currency the field's
   `pattern` is now `-?[0-9]+` (no decimal group), so a literal `-50.00`
   fails validation. **Fixed**: all four locales now say `-50`
   (en additionally dropped the hardcoded "£50" wording, matching the
   currency-neutral phrasing the other three locales already used).
   `guard-help-topics.sh` doesn't catch this — it checks route/front-matter
   consistency, not prose — so this needed a human/reviewer read.

2. **Latent generalization bug — the fix hardened the `/100` half of the
   old assumption but left the `{1,2}` half hardcoded.** The registry
   only holds 0- and 2-decimal currencies today, so nothing broke yet, but
   the obvious next MENA additions (KWD/BHD/OMR) are 3-decimal, and a
   hardcoded `[0-9]{1,2}` would silently reject a valid 3-decimal amount
   the same way the pre-fix `/100` silently corrupted a 0-decimal one —
   the same bug class this card exists to close. **Fixed**: extracted
   `httpx.MoneyPattern(decimals)`/`MoneyPlaceholder(decimals, example)`
   (generic over `decimals`, unit-tested including a 3-decimal case), and
   `MoneyPatternAttr`/`MoneyPlaceholderAttr` (below) as the template-facing
   wrappers, replacing 7 duplicated `{{ if eq currency.Decimals 0 }}…{{ end }}`
   ternaries across the two templates with one call site each.

   **Follow-on bug found while fixing #2, not by the reviewer**: routing
   `MoneyPattern`'s output through a hand-written `pattern="{{ moneypattern
   currency.Decimals }}"` made `html/template`'s contextual auto-escaper
   HTML-entity-encode the regex's own `+` to `&#43;` — confirmed
   empirically (a standalone repro, then the actual page tests). Harmless
   to a real browser (attribute values are entity-decoded before use as a
   pattern) but it broke the tests' literal-`+` assertions and was a
   needless departure from how this same regex reads everywhere else it's
   still hand-typed into markup (e.g. `index.html`'s `#pfand-amount`).
   Fixed by having `MoneyPatternAttr`/`MoneyPlaceholderAttr` render the
   **whole** `pattern="…"`/`placeholder="…"` attribute as
   `template.HTMLAttr` (confirmed via a throwaway `html/template` repro
   that only the whole-attribute form bypasses escaping — a
   `template.HTML` value substituted *inside* hand-written quotes still
   gets escaped identically to a plain string); template call sites became
   `{{ moneypattern currency.Decimals false }}` /
   `{{ moneypattern currency.Decimals true }}` (signed, for
   `#adjust-pounds`) with no surrounding `pattern="…"` in the template
   source.

3. **Test gap — `#opening-cash` (the one field the Go-side fix touched)
   had zero pattern/placeholder/label coverage**, because the label test
   inserts an *open* shift (`HasOpen` true), which hides the open-shift
   form entirely. **Fixed**: `TestShiftsPage_CarryForwardDisplayIsCurrencyAware`
   now asserts the full `#opening-cash` `<input>` tag (id + pattern +
   value together) under both GBP and IRT, plus the label's currency
   symbol both ways.

4. **Tautological assertion — the carry-forward test's positive check
   (`value="500"`) was also satisfied by the unrelated hidden
   `#opening-cash-minor` field** (prefilled straight from
   `CarryForwardMinor`, independent of the fix), so a formatter bug
   returning the wrong string could still coincidentally pass. Confirmed
   the negative half of the original assertion *did* correctly fail
   pre-fix, so this wasn't a false-passing test today — just weaker than
   it read. **Fixed**: folded into finding 3's fix (the full-tag
   assertion is scoped to `#opening-cash` specifically, not a bare
   substring).

5. **Label assertions couldn't detect a partial conversion** — passing as
   soon as *any one* of the four labels showed the new currency, with no
   check that the OLD symbol was gone everywhere. **Fixed**: both
   currency-aware tests now assert `!Contains(body, "(£)")` under the
   0-decimal currency, in addition to the positive symbol check.

6. **Tips-amount label had no currency cue at all** — the card's own
   acceptance criteria ("template the *labels*… off `currency.Display`")
   covers both files, and the reviewer confirmed there was never a
   hardcoded `£` there to begin with (so the issue's "hardcoded a GBP
   symbol" framing was slightly off for this specific field), but the
   card's intent was parity with `shifts.html`. **Fixed**: added
   `({{ currency.Display }})` to `reports.tips.form_amount`'s label,
   matching the convention `shifts.html` and `index.html` (`#pfand-amount`)
   already use.

Two additional, non-blocking notes were folded in as free improvements
while already in the code: a stale comment ("the 0.00 default") in
`shifts_page.go` now reads currency-neutral, and `currency_test.go` gained
a `{0, 2, "0.00"}` case (the single most common real input — every fresh
shift-open on a GBP shop with no carry-forward) and a `{1234, 3, "1.234"}`
case pinning the decimals-generic behavior finding 2 introduced.

**Not fixed here (pre-existing, unrelated to this diff, separately
tracked, confirmed by the reviewer as the only remaining gaps)**:
- ut-docs#1291 — the £50/£20/…/1p cash-count denomination grid is
  GBP-specific by nature (different currencies have entirely different
  physical denominations, not just a different symbol/decimal count).
- ut-docs#1289 — `shifts_api.go`'s `respondCloseSuccess` hardcodes
  `£%.2f` *and* untranslated English prose, a bigger i18n-key surface than
  this card's template-attribute fix.
- ut-docs#1290 — `promotions_page.go`/`settings_page.go` have the same
  hardcoded `/100` prefill bug on two other, unrelated pages.
- `internal/httpx/currency.go`'s negation of `math.MinInt64` (shared,
  pre-existing flaw already present in `FormatMoney`, unreachable for real
  money amounts) — noted by the reviewer for completeness only, not a
  real-world risk.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | clean |
| `go test ./...` (whole repo) | pass |
| TDD red→green, independently re-verified twice | reverting the production files (Go + templates), tests left in place, fails every new/changed assertion (or fails to build, for the `MoneyPatternAttr`/`MoneyPlaceholderAttr` addition); restoring returns all green |
| `guard-data-access.sh` / `guard-i18n.sh` / `guard-compliance-claims.sh` / `guard-help-topics.sh` / `guard-kiosk-engine.sh` / `guard-plugin-menu-read.sh` / `guard-webkit-version.sh` / `guard-kiosk-launch-flags.sh` / `guard-android-status-address.sh` / `guard-android-i18n.sh` / `guard-emoji-font.sh` / `guard-htmx-loaded.sh` / `guard-autofill-suppression.sh` / `check-brand-assets.sh` / `guard-makefile-version.sh` | all pass |
| `guard-docs-shots.sh` | flagged twice (app surface, then the manual prose fix); `make docs-shots` run both times, guard green after |
| Rendered HTML byte-for-byte sanity check | confirmed the `MoneyPatternAttr`/`MoneyPlaceholderAttr` refactor renders identical markup to the pre-refactor ternary version (dumped and diffed the actual `<input>` tags) |

## Follow-ups filed (out of scope here, per BA non-goals)

ut-docs#1289, ut-docs#1290, ut-docs#1291 (see above).
