# Review: release notes v0.34.9

Date: 2026-10-16 · Branch `docs/release-notes-v0.34.9`
Written by the `:54` build lane; reviewed independently by a Sonnet subagent.

## What shipped
Owner-facing "What's new" notes for v0.34.9 in en/de/tr/ar/fa. There have been 6 feat/fix merges since v0.34.8, so a release is due.
- New: translate mode (managers see where a label comes from, open it in Weblate, override it per shop).
- Improved: Settings → Tills shows the till's role and the main till's address; the register picker is relabelled; "primary till" now reads "main till".
- Left out on purpose: groundwork with no owner-visible effect yet. That covers the cloud-master pull of variants and barcodes, the apply-failure report, and the shared import-parser module. Also left out: a cloudsync change that only adds a test and fixes a comment.

## Findings (all fixed)
- Medium, all locales: the Registers link shows only to staff who can manage registers. The notes now say so.
- Medium, de: the terms now follow the German help: "Manager", "Zusätzliche Kasse", "Kassenplatz für Auszahlungen", „Register“.
- Low, all locales: the main till's address shows on satellites too, not only on additional tills. The notes now say both.
- Low: "text key" is borderline jargon. Accepted, because it matches the help topic's own wording.

## Verified
- Each bullet was checked against the merged diffs, the help topic `translate-mode` in all five locales, and the locale keys.
- `scripts/ci/guard-release-notes.sh v0.34.9` and `go test ./internal/releasenotes/` pass.

## CI follow-up
- `pages-shuffle (seed 22)` failed `TestSettingsPage_TillsCardShowsRoleAndMainTillAddress`. Settings → About renders the latest note, and the English bullet's literal "**This till: Main till**" matched the test's check on a joined till's page. The bullet was reworded ("the card now starts with the till's role: main till, additional till or satellite"). The full `go test -shuffle=22 ./internal/pages` now passes locally.
- Rebased onto `main` after #2021 merged (phone order cards no longer show a blank strip). Added a Fixed bullet for it in all five locales, checked against that PR's description and its spec. The fa heading matches earlier notes ("رفع اشکال‌ها").
