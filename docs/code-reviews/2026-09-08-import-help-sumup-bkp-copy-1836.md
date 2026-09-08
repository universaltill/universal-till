# Code review: import.help/import.file copy — SumUp naming, German .bkp restore (ut-docs#1836)

**Date:** 2026-09-08
**Branch:** `fix/1836-import-help-sumup-bkp-copy`
**Reviewer:** independent fresh-context Sonnet subagent (`complexity:easy` routing), isolated worktree

## What shipped

The German pilot merchant reported the import screen tells him only CSV
can be imported. He was reading the German UI, and it was wrong: the
German **language pack** (`ut-plugin-language-de`) had dropped `.bkp`
entirely from both `import.file` and `import.help` — fixed and merged
separately in that repo (`ut-plugin-language-de#194`), not part of this
diff. Core's own English source (and ar/fa/tr) had a narrower, related
gap: `import.help` named "Loyverse and Square" as auto-detected but not
"SumUp" (SumUp detection was added to `catimport.DetectFormat` by an
earlier card, #581, and the help text was never updated), in every core
locale.

This change:

- `web/locales/{en,ar,fa,tr}.json`: `import.help` now names SumUp
  alongside Loyverse and Square. The `.bkp` clause was already correct in
  all 4 core locales — audited, no drift found there.
- `web/help/{en,ar,fa,tr}/catalog.md`: the Import step now names which
  systems are auto-detected, matching the in-app copy (the manual didn't
  previously name any system, so it wasn't factually wrong, just less
  complete). Screenshots regenerated (`make docs-shots`) since the guard
  hashes topic markdown content even though no screen's pixels changed.
- `internal/pages/import_help_format_names_test.go` (new): two
  regression tests — every core locale's `import.help` names every
  format `catimport.DetectFormat` can recognise (a hand-maintained list,
  deliberately not derived from `DetectFormat`'s source — see the file's
  own comment for why), and `import.file`'s claimed formats match the
  file input's real `accept=` attribute (`.bkp`).

## Independent review — verdict: SAFE TO MERGE

Full transcript kept by the orchestrator; summary here.

**TDD re-verified independently:** reverted only the locale-file changes
(test file left in place), confirmed `TestImportHelpCopy_
NamesEveryAutoDetectedFormat` fails in all 4 locales with the exact
"does not mention SumUp" message, confirmed `TestImportFileFormats_
MatchTheRealFilePicker` is correctly unaffected by that specific revert
(the `.bkp` claim was already correct pre-fix), then restored and
confirmed both pass again.

**Verified beyond automated tests:**
- Read the full diff (not just hunks) for both locale files and the
  manual topic — confirmed the pre-existing `.bkp` clause is
  byte-identical before/after in every file; no accidental duplication or
  reordering from the string-replace-based edits.
- Read `web/help/en/catalog.md` in full, not just the changed sentence —
  confirmed the surrounding steps remain accurate and the amendment
  satisfies the "manual ships with the feature" standing rule (ut-docs#324).
- Visually inspected one of the regenerated screenshots
  (`web/help/img/ar/till-designer.png`) — renders correctly; the tiny
  byte-level diffs across a few unrelated topics' images (all <0.05% size
  change) are re-encoding noise from the regen, not a real regression.
- Cross-checked `namedAutoDetectedFormats` against `catimport.
  DetectFormat`'s actual branches — currently accurate (loyverse, square,
  sumup; generic/generic-erp correctly excluded as non-named fallbacks).
- Confirmed both target HTML files (`import.html`, `setup.html`) have
  exactly one `accept="..."` attribute each, single-line and
  double-quoted, so the new test's simple string-search parsing is
  correct today (flagged as fragile in the abstract, not a real problem
  for these two files as they stand).
- No real client/shop name in any test data.

### Findings

**nit (addressed in this branch):** the test file's own comment now
explicitly discloses a residual limitation the review surfaced — the new
guard catches a *known* named format's mention going missing, but does
not mechanically catch the *opposite* drift (a brand new `DetectFormat`
branch added with no matching entry here and no copy update). That
direction still relies on reviewer/dev diligence, same as before this
card. Added one paragraph to the code comment saying so plainly, so a
future reader doesn't assume this test is a complete guard against
either direction of drift.

**nits — not fixed, judged genuinely low-risk:**
- The HTML `accept="..."` parsing is a simple string search (find
  `accept="`, then the next `"`) rather than a real HTML parser — would
  silently grab the wrong attribute if the markup ever gained a second
  file input or switched to single-quoted attributes. Correct for both
  files as they exist today; not worth a full HTML-parsing dependency for
  this one check.

## Verified beyond automated tests (orchestrator, before handoff to review)

- Full `go build ./...`, `go vet ./...`, `gofmt -l .` clean.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` all
  green.

## Safe to merge

Yes. No blocker or should-fix findings beyond the one comment addition
above, already applied.
