# Review: German manual says "Kassierer" like the till's UI (ut-docs#5244)

Date: 2026-11-01 · Lane: `lane:cloud-41` · Branch: `fix/5244-de-help-kassierer`

## What shipped

- `web/help/de/display.md`, `web/help/de/printing.md`: the three "Kassenkraft"
  occurrences now say "Kassierer" (grammar adjusted: "ein Kassierer", "der
  Kassierer"), the word the German UI uses (`ut-plugin-language-de`
  `locales/de.json`: `users.role.cashier`, `shifts.cashier` = "Kassierer";
  the pack never says "Kassenkraft").
- `display.md`, `users.md`: "manager-PIN-geschützte(n)" → "Manager-PIN-…",
  matching the UI's "Manager-PIN" (`auth.exit_to_os.pin_label`) and the rest
  of the manual (the card's optional item).
- `scripts/ci/help-terms.txt`: new entry `de|kassenkr(aft|äfte)|Kassierer`,
  so `guard-help-terms.sh` (#5243) stops the word coming back.

## Review

Built by a Sonnet dev subagent; reviewed by Opus (different model, per
MODEL-ROUTING). Word-level diff read in full: only the five intended word
swaps plus the two-line term entry; no other sentence text changed.

Findings: none. German grammar of the edited clauses checked
("ein Kassierer wird nach der PIN eines Managers gefragt", "ein Kassierer
sieht ihn nie", "der Kassierer druckt bei Bedarf").

## Verified

- Guard red→green re-run by the reviewer: with the new entry and the old
  prose, `guard-help-terms.sh` exits 1 naming `display.md:29` and
  `printing.md:24`; with the fix it passes.
- `guard-help-terms_test.sh`, `guard-help-drift.sh` (baseline untouched),
  `guard-help-topics.sh`, `guard-help-breadcrumbs.sh` (+ `_test`) pass.
- No shell script changed (shellcheck not applicable). German is not a
  docs-shots locale, so no screenshots change.

## Verdict

Safe to merge. Deferred: none.
