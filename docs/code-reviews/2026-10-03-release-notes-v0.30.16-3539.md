# Review: release notes v0.30.16 catch-up (ut-docs#3539)

**Date:** 2026-10-03 · **Branch:** `fix/3539-release-notes-v0.30.16` · **Reviewer:** independent Fable subagent (author: Opus 5.5)

## What shipped
`web/release-notes/{en,de,tr,fa,ar}/v0.30.16.md` extended with the shop-visible
changes merged to `main` since the note was last written (2026-10-02 06:48 UTC,
merge `0383fd31e`): 7 New, 8 Improved, 15 Fixed bullets, same count, order and
meaning in all five languages; date bumped to 2026-10-03. Content only — no code.

Deliberately left out (not shop-visible yet or not owner-facing): CSP slice 2,
PT shadow mode (paused market), personal display part 1 (no UI), the no-sale
backend slice (no button until ut-docs#3560), plugin-developer-only manifest
checks, CI/test-only merges.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | "More from your cloud account" claimed net quantity can be set from the cloud; only the till-side directive merged, ut-cloud has no `set_net_quantity` | Fixed — clause dropped in all 5 languages |
| 2 | minor | "basket-panel card" names no card; the form is Reset divider on the Display card | Fixed in all 5 |
| 3–9 | minor/nit | Translated UI names not matching the locale strings (de Optionssets, Verkaufshistorie löschen, Retoure; fa دسترسی‌ها, مجموعه‌های گزینه, پاک‌کردن تاریخچهٔ تراکنش; ar استرجاع) | Fixed |
| 10 | nit | en "New customer" → "New Customer" (UI label) | Fixed |
| 11 | nit | Pre-packed unit-price setting is not country-gated; "(UK)" names the legal driver | Accepted — matches the existing UK shelf-label bullet |

A 15th Fixed bullet (duplicate till names refused, merged after the review) was added afterwards; it restates that merge's own help text.

Reviewer traced each of the 29 added English bullets to its merge and found no
omission among user-facing merges.

## Verified
`go test ./internal/releasenotes/ ./web/...` (incl. `TestBuiltin_NotesAreOwnerLanguage`),
`guard-release-notes.sh v0.30.16`, `guard-release-notes_test.sh`,
`guard-compliance-claims.sh`, `guard-competitor-naming.sh`, `guard-i18n.sh` — all green.

## Verdict
Safe to merge. Next: dispatch `release.yml` (`bump=patch`) once `main` is green.
