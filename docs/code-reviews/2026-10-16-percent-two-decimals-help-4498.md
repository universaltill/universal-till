# Review: two-decimal percent limit in the promotions, payments and plugins help topics (ut-docs#4498)

Date: 2026-10-16 · Branch: `docs/4498-percent-two-decimals-help` · Lane: cloud-54
Card: ut-docs#4498 (follow-up from the #4446 review) · Reviewer: independent Fable subagent (author: Opus 5.5)

## What shipped

Every percent field that uses `{{ percentpatternlocal }}` takes at most two
decimals: both the browser pattern and `httpx.ParsePercentBP` on the server
refuse a third. #4446 added that limit to the tax-code and country-settings
topics. This change adds it to the remaining topics that describe such a
field, in all five help locales (en, de, tr, ar, fa):

- `promotions.md`: the percent-off value (`12.25` accepted, `12.125` refused).
- `payments.md`: the provider-fee percent (`2.75` / `2.875`). In English the
  clause goes in the **Provider fees** bullet. The de/tr/ar/fa translations
  have no "Configuring payment methods" section (known drift, already in
  `help-drift-baseline.json`), so there the clause goes in the step-3
  parenthetical that points to the fee settings. That parenthetical is the
  only place those translations mention provider fees.
- `plugins.md`: the tax plugin's takeaway-rate editor (`7.25` / `8.875`).

`internal/httpx/percent_help_test.go` now covers all five topics. It requires
both examples in every locale's copy of each topic, and it checks that each
example really is accepted or refused by `PercentPatternLocal`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | process | Pre-review commit message was a WIP placeholder | Squashed into a real commit message |
| 2 | process | No review record yet | This file |
| 3 | nit | tr/fa phrasing differs slightly from their tax-codes sentence ("edilmez" / "نه") | Accepted: both are correct |
| 4 | nit | de examples use a dot while de/payments.md shows `2,50` elsewhere | Accepted: this matches the de tax-codes convention from #4446, and the test pins the dot form |
| 5 | nit | The test's substring check is file-wide, not tied to a sentence | Accepted: these numbers appear nowhere else in those files |
| 6 | nit (pre-existing, out of scope) | A 3-decimal fee on the payments settings page reports a "range" error rather than a format error | Not changed: the refusal is correct, only its label is imprecise |

## Verified

- The reviewer traced each field's server path: promotions → `promotions_page.go:95`, payment fee → `settings_page.go:1077`, takeaway editor → `plugin_settings_page.go:359`. All three go through `ParsePercentBP` (`ParseMoneyMajor(raw, 2)`), which refuses more than two fraction digits.
- TDD: with the help edits stashed, the extended test fails (for example `web/help/tr/plugins.md: missing the two-decimal example 8.875`). Restored, it passes.
- `go build ./...`, `go vet ./internal/httpx ./internal/pages`, `go test ./...`: every package passed except `internal/plugins`, which hit the 600 s test timeout under container load. Re-run on its own with a longer timeout, it passed (`ok … 986.8s`). The diff touches nothing that package reads.
- Guards: `guard-help-drift` (baseline untouched; every insert is inside a sentence, so no structural count changes), `guard-help-topics`, `guard-compliance-claims`, `guard-competitor-naming`, `guard-mail-addresses`, `guard-i18n`, `guard-rtl-logical-css`, `guard-core-neutral`, `guard-readme-local-links` all pass.
- No UI surface and no locale key changed.
- `guard-docs-shots` hashes topic markdown, so `make docs-shots` was re-run (120 shots passed). Every PNG came out byte-identical, and only the 8 topic hashes (plugins and promotions × en/fa/ar/tr) in `web/help/img/manifest.json` changed. payments.md has no screenshot route.

**Verdict:** safe to merge.
