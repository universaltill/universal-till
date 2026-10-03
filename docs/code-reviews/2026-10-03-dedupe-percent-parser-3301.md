# Review — dedupe percent↔basis-point parser onto internal/taxrate (ut-docs#3301)

## Summary

`internal/taxrate.ParsePercent`/`FormatPercent` (ut-docs#3259, the shop
default tax rate's parser) is the canonical "percent string ↔ basis points"
pair. Two more copies of the same grammar existed:
`country_settings_page.go`'s `parsePercentAsBP`/`formatBPAsPercent`, and
`tax_codes_page.go`'s `parsePercentToBP`. Both of the latter two already
delegated their actual digit-grammar to `httpx.ParsePercentBP`, not an
independent hand-rolled parser — but that's still a second parser from
`internal/taxrate`'s point of view, with its own subtly different bounds.
The risk, per the card: "If the copies drift, one form will accept a rate
that another refuses."

## Changed here

- `internal/pages/country_settings_page.go`: `parsePercentAsBP` now calls
  `internal/taxrate.ParsePercent` instead of `httpx.ParsePercentBP`, keeping
  its own `TrimSpace` + empty-string-returns-0 wrapper and its own error.
- `internal/pages/tax_codes_page.go`: `parsePercentToBP` now calls
  `internal/taxrate.ParsePercent` instead of `httpx.ParsePercentBP`,
  dropping the now-redundant manual `bp > 10000` check (`taxrate.MaxBP` is
  the same inclusive 0–100% bound).
- `formatBPAsPercent` (country settings) deliberately **not** unified with
  `taxrate.FormatPercent`: that one trims every trailing zero ("8.50" →
  "8.5"), but this page's row display is pinned to always show two decimals
  for a fractional rate by `e2e/tests/country-settings-record-dialog-2187.spec.ts`
  ("12.5" saved → "12.50%" shown), and it's also reused at
  `settings_page.go:1044` for the payment-fee elevation summary. Unifying it
  would have been a real regression on two screens, not a cleanup.
- Tests added to pin the two behavior changes this dedup produces (both
  verified real, not tautological — see below):
  - `parsePercentAsBP`/`parsePercentToBP` now accept a trailing bare
    separator with no fraction digits ("19." → 1900bp), matching
    `internal/taxrate.ParsePercent`'s own already-documented "harmless"
    case. The old `httpx.ParsePercentBP`-based wrappers rejected it.
  - `parsePercentAsBP` now refuses anything over 100% ("100.01", "150").
    The old wrapper enforced no upper bound of its own ("range checks are
    the caller's" — `httpx/currency.go`), and country-settings added none,
    so a tax rate above 100% could previously be saved. Found during
    review; see findings below.
- `countrysettings.error.tax_invalid` updated in all 4 core locales
  (en/fa/tr/ar) from "zero or more" to "between 0 and 100", since the field
  now actually enforces an upper bound and the old message was wrong for
  that rejection path.
- Two stale comments fixed (`percent_comma_2954_test.go`,
  `e2e/tests/percent-comma-2954.spec.ts`, `country_settings.html`) that
  said every percent field reads `httpx.ParsePercentBP` — now true only for
  promotion and payment-fee percent, not tax rates.
- Promotion percent (`promotions_page.go`) and payment-fee percent
  (`settings_page.go`) were **not** touched — they aren't tax rates, and
  not what the card's drift risk is about. Noted here per the reviewer's
  request rather than silently narrowing the card's own "only one
  parse/format pair" phrasing.

## Independent review (Opus 5.5, fresh context)

Ran `go build ./...` (clean), `go vet ./...` (clean),
`go test ./internal/pages/... ./internal/taxrate/... ./internal/httpx/...`
(all green) independently. Re-verified the TDD claim by reverting just
`country_settings_page.go`/`tax_codes_page.go` to `main` while keeping the
new tests, confirming both new/changed tests fail with the expected error
(`parsePercentAsBP("19.") errored: invalid percent "19."`,
`parsePercentToBP("19.") = 0, invalid rate; want 1900, nil`), then restored
and confirmed green again — the tests exercise the real code path, not a
tautology.

Findings:

1. **Fixed — country-settings tax rate had no upper bound before this
   change, and nothing tested or told the user that.** `httpx.ParsePercentBP`
   never bounded above 100% itself, and `country_settings_page.go` added no
   check of its own, so a rate like "150" saved as 15000bp on `main`. Moving
   to `taxrate.ParsePercent` (MaxBP, 0–100% inclusive) now correctly refuses
   it — the same bound tax codes and the shop default already enforce — but
   the dev pass hadn't called this out, no test pinned it, and the existing
   error copy ("zero or more") was wrong for this rejection. Fixed: test
   cases added (`"100"` succeeds at 10000bp, `"100.01"`/`"150"` error), all
   4 locale strings reworded, doc comments updated.
2. **Recorded, not fixed — the card's literal "only one parse/format pair"
   AC is met for parsing tax rates, not for every percent field or for
   formatting.** `httpx.ParsePercentBP` still parses promotion and
   payment-fee percent (different fields, not tax rates — correctly out of
   scope). `formatBPAsPercent` and `taxrate.FormatPercent` both still exist
   because their display conventions are genuinely different by design
   (confirmed against the e2e spec and the second `formatBPAsPercent` call
   site at `settings_page.go:1044`), not because of leftover drift. Tax
   codes display "12.5" while country settings display "12.50%" for the
   same rate — a real but pre-existing and out-of-scope inconsistency,
   worth a future card if anyone wants to unify display, not this one.
3. **Fixed — two comments claiming every percent field reads
   `httpx.ParsePercentBP`** were stale now that tax rates go through
   `taxrate.ParsePercent`. Reworded to name both parsers and which fields
   use which.
4. **Checked, no other divergence matters for a tax rate:** leading zeros
   (taxrate strips them, httpx didn't — harmless, both still bound to
   0–100%), overflow (none, taxrate's 3-digit whole-part cap runs before
   arithmetic), sign/exponent/NaN/Inf/bare-fraction/3+-fraction-digits/inner
   spaces (both reject identically). The browser's own `PercentPatternLocal`
   HTML pattern already blocks a bare trailing separator client-side, so
   "19." is only reachable via a direct POST — lower real-world exposure,
   but still worth the dedup since the whole point is forms agreeing.
5. **Checked, no other callers:** repo-wide grep found no other call sites
   of `parsePercentAsBP`, `parsePercentToBP`, or `formatBPAsPercent` beyond
   the ones already accounted for above. Dropping the redundant
   `bp > 10000` check in tax codes is correct (`MaxBP` is inclusive, 100 is
   still allowed, matching the ut-docs#259 fix this function's comment
   documents). The `int64`↔`int` conversions are safe at this range. No
   file writes, so no `os.MkdirAll`/`paths.Data` concern. No new
   user-facing strings besides the corrected error message.

## Verified beyond automated tests

Full `go test ./...` gate (80 packages) green before and after the
reviewer's finding-1 fix; `guard-data-access.sh` and `guard-i18n.sh` both
green after the locale edits. No UI surface was touched (no template/JS
behavior change — the one display function in scope was deliberately left
alone), so no driven `/run` pass or screenshot applies here; recorded
explicitly rather than silently skipped.

## Verdict

Safe to merge. Scope: 8 files touched (4 Go source/test in
`internal/pages`, 4 locale JSON), all directly in service of the card; no
drive-by edits. One real pre-existing bug (unbounded country-settings tax
rate) found and fixed as part of this dedup, pinned by new tests.
