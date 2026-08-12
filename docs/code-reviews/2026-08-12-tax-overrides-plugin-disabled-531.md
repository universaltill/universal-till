# Code review: distinct warning when ut-plugin-tax-de is installed but disabled

**Date:** 2026-08-12
**Card:** ut-docs#531
**Scope:** `internal/pages/import_page.go`, `internal/pages/import_page_test.go`,
`web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,fa,tr}/catalog.md`

## What shipped

`mergeTakeawayOverrides` only ran when `ut-plugin-tax-de` was installed
**and** active. A shop that imports its catalog before enabling the plugin
(a natural setup order — import first, then turn on the country-specific
tax plugin) got correct tax codes but a silent, empty
`takeaway_rate_overrides`: nothing told the merchant that re-enabling the
plugin later wouldn't retroactively fix that import's rows.

The fix distinguishes **installed-but-disabled** from **not-installed-at-all**
(the latter stays silent, per #512's original design — a shop with no German
tax plugin genuinely has nothing to configure):

- `import_page.go`: when `PluginActive` returns false, a new
  `pluginRepo.GetPlugin(ctx, taxDePluginID, "")` existence check decides
  between silence (not found) and a new `overridesPluginDisabled` warning
  path (found).
- New i18n key `import.status.tax_overrides_plugin_disabled` in all four
  locales, rendered via the existing `row-warn-icon` summary-line pattern
  (same markup as the sibling `tax_overrides_not_saved` warning).
- New `takeaway_overrides_plugin_disabled` audit field.
- `web/help/{en,ar,fa,tr}/catalog.md` gained a step describing the
  behavior, per this repo's "manual ships with the feature" rule.
- New test `TestImport_TaxOverridesWarnsDistinctlyWhenPluginDisabled`
  (TDD — confirmed it fails pre-fix, passes post-fix).

## Independent review (fresh-context Sonnet subagent — complexity:easy)

Given the full diff scope, the task context, and told explicitly to run
things and find real problems. It did:

- Read the changed logic in full surrounding context, not just the diff,
  and traced all five branches of the new if/else-if chain (active /
  active-check-error / not-active-but-check-error / not-active-found /
  not-active-not-found) — confirmed sound, and confirmed the branch only
  triggers when there's actually something to write (`len(takeawayOverrides)
  > 0`), so it never fires spuriously.
- Investigated a genuine question on its own initiative: does
  `PluginRepo.GetPlugin(ctx, id, "")` (empty version) risk ambiguous-row
  behavior if multiple plugin versions exist? Checked the schema
  (`internal/db/migrations/001_init.sql`) — `plugins.id` is the primary
  key (not composite with version), so at most one row per plugin id can
  ever exist; the concern doesn't apply. Verified, not assumed.
- **Independently re-verified the TDD claim itself**, not on the commit
  message's word: reverted only `import_page.go` to its pre-fix version
  (kept the new test), reran the new test, confirmed it fails with the
  expected "no warning" symptom, then restored the fix and confirmed all
  `TestImport_Tax*` tests pass again. Working tree left clean afterward.
- Ran `go build`, `go vet`, the targeted test subset, and the **full**
  `go test ./...` (all ~40 packages green, no failures/skips).
- Ran `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-kiosk-engine.sh` — all green.
- Read the actual locale-file diffs (not just checked key presence) —
  confirmed genuinely translated, equivalent-meaning prose in all four
  locales, no machine-placeholder text.
- Checked the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll` on a file-write handler; cwd-relative path
  instead of `paths.Data(...)`) — grepped and confirmed both
  structurally N/A (no file writes, no path literals in this diff).
- Checked the audit-shape change doesn't break any consumer (`audit_page.go`
  renders audit JSON generically, no fixed decode struct — non-breaking).
- Checked the new warning's HTML reuses the existing `row-warn-icon`
  class/CSS token, no new hardcoded styling.
- Checked for real client/shop names or secret-shaped literals — none;
  test fixtures use RFC 2606 `example.invalid` and an obviously-fake
  `sha256`.

**One minor/nit, not blocking, not fixed**: the new branch treats any
existing plugin row as "installed but disabled" without checking
`install_state` (e.g. a hypothetical `broken`/`installing` state would
also read as "disabled"). Reviewer verified this is theoretical, not a
real gap today — `handleDisablePlugin` only ever flips `is_active`, and no
code path currently sets `install_state` to those values. Noted for
awareness, not filed as a backlog item (not a real-world scenario yet).

## Verification (self, after review — no fixes were needed)

Nothing in the review was blocking, so no fix round was required. Re-ran
personally for this record: `go build ./...`, `go vet ./...` clean;
`go test ./... -count=1` — all packages `ok`, no failures; `guard-i18n.sh`,
`guard-data-access.sh`, `guard-help-topics.sh`, `guard-kiosk-engine.sh` all
green.

## Verdict

**Safe to merge as-is.** No blockers or majors. One accepted, verified,
non-blocking minor noted above.
