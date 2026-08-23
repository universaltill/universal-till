# Code review: Settings → Data-management panel manual coverage

**Card:** universaltill/ut-docs#777
**Date:** 2026-08-23
**Author:** Farshid Mirza (autonomous pipeline cycle)
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier)

## Summary

The Settings → Data-management card on `/settings` (clear transaction
history, reset-archive restore/purge, catalog cleanup, GDPR customer
erasure) had zero manual coverage — `display.md` documented the rest of
`/settings` (language/display, tills, shop type, sample-data removal) but
stopped short of these four actions, found during ut-docs#640's independent
review. This closes the gap.

## What changed

- `web/help/{en,fa,tr,ar}/display.md`: four new numbered steps (10–13)
  documenting clear-transaction-history, reset-archive restore/purge
  (including the retention-window purge-block behavior from #698), catalog
  cleanup, and GDPR customer erasure ("anonymised, not deleted" semantics
  from #640) — translated into all three non-English shipped locales in the
  same change, per `reference/translation.md`'s same-change rule.
- `web/ui/pages/settings.html`: added `{{ helpLink "display" }}` next to the
  Data-management card's own `<h2>`, following the exact convention already
  used by the backups/claim/payments/printing/reports cards on the same
  page — a documentation-linking hint, not a competing `routes:` claim
  (`universal-till/CLAUDE.md`).
- `web/help/img/**` + `manifest.json`: regenerated via `make docs-shots` for
  `guard-docs-shots.sh` freshness (required since `settings.html` is part of
  the hashed app surface).

No Go/JS/SQL logic changed — pure documentation + one template-hint line.

## What was verified

- Every new English claim cross-checked against `web/locales/en.json`'s
  `settings.data.*` keys and the actual `settings.html` markup: confirm
  phrases ("RESET"/"RESTORE"/"PURGE"/"CLEANUP"), the retention-window
  purge-block behavior (`Purgeable` gate, lines 362–371), and the GDPR
  "kept for records but no longer show who they belonged to" wording all
  match what ships.
- `{{ helpLink "display" }}` placement confirmed on the Data-management
  card's own heading, correct topic id (`display` already claims
  `routes: [/settings]`), same syntax already proven working elsewhere in
  this file.
- Compliance wording (ADR-0040): new prose describes only factual
  capability ("erase a customer's personal details on request"), no
  compliance-certification outcome claims.
- `gofmt -l .` — clean. `go build ./...` — clean.
  `go test $(go list ./... | grep -v '/internal/plugins$')` — all packages
  pass.
- `scripts/ci/guard-help-topics.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` — all pass.
- Real render check: `make docs-shots` drove a real headless-Chromium visit
  to `/settings` (all 4 locales) via Playwright — page rendered without
  error, confirming the template change doesn't panic at runtime; the
  captured `display.png` itself is unchanged only because the screenshot
  viewport (1024×600, not full-page) doesn't reach the Data-management
  card, which sits below the fold — a benign, explained non-finding, not a
  skipped check.

## Independent review

A fresh-context Sonnet subagent (no prior context) re-read the full diff,
re-verified every English claim against the same locale keys/markup
independently, checked all 4 translations for fluency/consistency/leftover
English artifacts, re-ran `gofmt`, `go build`, and all four docs/i18n/
compliance guards itself, and confirmed scope was exactly the 15 files
listed above with no logic touched. **Verdict: PASS, no findings.**

## Non-goals (unchanged from the card)

- Not a UI/behavior change beyond the `helpLink` hint — manual-writing only.
- No business decision needed, per the card's own text.
