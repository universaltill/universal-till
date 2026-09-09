# Review: help reports.md missing sections (tr/ar/fa) — ut-docs#1889

**Card:** universaltill/ut-docs#1889 (`complexity:medium`, `i18n`)
**Repo/branch:** `universal-till`, `fix/1889-help-reports-missing-sections`
**Implementer:** Sonnet (inline, docs-only change)
**Reviewer:** Opus, fresh-context subagent, independent of the implementation

## What shipped

`web/help/en/reports.md` has three sections that `tr`/`ar`/`fa` `reports.md`
did not have at all (not partially untranslated — entirely absent):

- "Cancellations, and who ran the close"
- "Dine in / takeaway breakdown"
- "Reconciling a card payment (receipt detail)"

Translated all three into Turkish, Arabic and Farsi and inserted them at
the same position as the English original, so all four locale files now
have the same 15 sections in the same order (was 12 in tr/ar/fa, missing
the same 3 in each).

Translations follow this file's own established convention for the
printed Z-report's literal, non-localized vocabulary — confirmed against
`internal/pages/eod_api.go`'s comments and output strings (`STORNOS`,
`Erstellt von`, `Anmerkung`, `BY ORDER TYPE`, `Dine in`, `Takeaway` are
printed verbatim regardless of till locale) — the same pattern already
used for `GUTSCHEINE`/`CASH RECONCILIATION` elsewhere in the same file:
keep the literal label bolded and verbatim, gloss it in the target
language in parentheses on first use, translate the surrounding prose
normally.

Glosses were sourced from each locale's own `web/locales/<lang>.json`
(`basket.order_type.dine_in`/`.takeaway`, `reports.eod.by_order_type`,
`journal.detail.terminal_id`/`.trace_id`, `reports.eod.operator`) and
from each file's own existing cross-references ("Audit page", "Journal")
rather than invented fresh, so terminology stays consistent with what's
already on screen and already in the rest of the document.

## Independent review (Opus, fresh context)

Ran, for real, not just read the diff:

| Check | Result |
|---|---|
| `bash scripts/ci/guard-help-topics.sh` | PASS |
| `bash scripts/ci/guard-compliance-claims.sh` | PASS (308 files) |
| `bash scripts/ci/guard-i18n.sh` | PASS |
| `bash scripts/ci/guard-docs-shots.sh` | **FAIL initially** |
| `go test ./internal/manual/...` | PASS |
| `go build ./...` | PASS |
| `grep -c '^##' web/help/{en,tr,ar,fa}/reports.md` | 15/15/15/15 |

Confirmed the `guard-docs-shots.sh` failure was caused by this change
specifically (not pre-existing) by cloning the parent commit into a
scratch dir and re-running the guard there, where it passed.

### Findings

1. **Blocker — `guard-docs-shots.sh` failed.** The topic markdown hash
   changed but `web/help/img/manifest.json` wasn't regenerated. This is a
   CI-blocking guard in `.github/workflows/ci.yml`'s `build` job.
   **Fixed:** ran `make docs-shots` and committed the refreshed manifest.
   An unrelated 2-byte re-encode of `web/help/img/en/sell.png` came along
   with the full regeneration run (Playwright regenerates all 112
   screenshots on each run, not just the changed topic) — confirmed via
   `file` that dimensions and format are unchanged; this is expected
   incidental noise from the regeneration tool, not a regression.

2. **Real defect — Arabic mistranslation.** `web/help/ar/reports.md`
   translated "a future till update" as `لتحديث مستقبلي للكاشير`
   (for the cashier/operator) instead of `للصندوق` (the till). This
   file's own glossary uses `الصندوق` for the till (26 other occurrences)
   and reserves `الكاشير` for the operator — its own existing line uses
   both correctly in one sentence. **Fixed.**

3. **Convention nit — Arabic dropped the verbatim labels in one closing
   sentence.** The English closes with the literal printed labels ("just
   the **Dine in** row, not an empty **Takeaway** line"); the Arabic had
   rendered these back into the glosses instead of keeping the literals,
   unlike the tr/fa versions and the rest of the same Arabic section.
   **Fixed** to match.

4. **Accepted, out of scope — minor "refund" term drift.** `ar`/`fa` use
   a slightly different word for "refund" than their own `<lang>.json`'s
   `reports.refunds` key, but this drift already existed in both files
   before this change (their own neighbouring sections already used the
   same wording this new text uses) — consistent with its immediate
   neighbours, not a regression this PR introduces. Left as-is; a
   dedicated glossary pass across the whole file is separate scope.

5. **Cosmetic, accepted — Turkish reuses "mutabakat" for two related but
   distinct English terms** (the reconciliation line vs. the settlement
   report) where ar/fa keep them distinct, and localizes "QR pay" while
   ar/fa keep it verbatim. No locale key exists to arbitrate either; both
   readings are natural and left as the implementer's editorial choice.

### What was verified correct

- Positioning matches `en`'s section order exactly in all three locales.
- The literal-label convention (verbatim + bolded + glossed once) applied
  consistently for `STORNOS`, `Erstellt von`, `Anmerkung`,
  `BY ORDER TYPE`, `Dine in`, `Takeaway` in all three files.
- Every literal-vocabulary claim re-verified against
  `internal/pages/eod_api.go`'s actual output strings and
  `internal/pages/eod_api_test.go`'s `"System"` fixture — not asserted on
  trust.
- Gloss-to-locale-JSON match is exact in all three locales for every term
  checked.
- Meaning survives translation: void-vs-refund distinction, "never mixed
  into one figure", zero-activity day omits the section entirely, mixed
  dine-in/takeaway sales split without a third row, masked card number +
  approval code + terminal + trace ID, and the today's-payment-methods
  list — no dropped or invented clauses, no number/technical-term drift.
- Markdown integrity: `**`/backtick counts balanced in all four files,
  heading levels consistent, files end with a newline.
- Scope: exactly the 3 locale help files plus the screenshot manifest
  regeneration — no Go, template, or locale-JSON files touched.

## Verified beyond automated tests

- Rendered each new section through `internal/manual`'s real Load+Topic
  pipeline (not just eyeballing the markdown source) to confirm clean
  `<h2>` HTML output with no unrendered `**` — this is the closest
  equivalent to a "look at it" check available for a docs-only change
  with no CSS/layout surface touched.
- Full `go test ./...` and `go build ./...` — clean, no regressions in
  any other package.

## Safe-to-merge verdict

**Safe to merge** after the two fixes above (blocker + Arabic defect)
were applied and the gate re-run clean.

## Explicitly deferred

- Item 4 above (a pre-existing minor "refund" terminology drift across
  `ar`/`fa` reports.md vs. their own locale JSON) — real, but out of
  scope for this card; worth a dedicated glossary-consistency pass across
  the whole help tree rather than a one-off fix here.
