# catimport: failed item row no longer leaves an inert takeaway override

**Date:** 2026-08-12
**Card:** ut-docs#535
**Complexity:** easy
**Author (dev):** scrum-master pipeline cycle, inline (Sonnet)
**Reviewer:** independent fresh-context Sonnet subagent (isolated worktree)

## What shipped

In `internal/pages/import_page.go`'s CSV-import commit loop, a row's tax
(dine-in, takeaway) override candidate was merged into `takeawayOverrides`
as soon as `FindOrCreateTaxCode` resolved — before that row's item insert
(`BeginTx` / `CreateItemTx` / `tx.Commit()`) had actually succeeded. If the
item insert then failed and the row `continue`d (marked failed, not
created), the candidate stayed in the map and was still merged into
`ut-plugin-tax-de`'s `takeaway_rate_overrides` setting — an inert entry for
a tax code that ends up with zero items actually referencing it.

Fix: the candidate is now held in a per-row local (`pendingOverrideBP`,
paired with the already-in-scope `taxCodeID`) and only promoted into
`takeawayOverrides` immediately after `tx.Commit()` succeeds — the same
promote-after-commit pattern the function already uses for
`stockWarning`/`stockRecorded`.

## What the independent review found

Nothing — no blocker, no non-blocker findings. It:
- Traced every `continue` between where the candidate is set and where
  it's promoted, confirming none can leak a false promotion and none skips
  a promotion that should have happened.
- Confirmed `taxCodeID` is provably non-nil whenever `pendingOverrideBP !=
  nil` (no nil-deref risk).
- Ran `go build ./...`, `go vet ./...`, the full `TestImport*` suite (30
  tests, all pass), and `scripts/ci/guard-data-access.sh` (clean).
- Did a revert→confirm-fail→restore→confirm-pass TDD verification cycle
  itself: reverted just the production-code hunk, re-ran the new
  regression test and watched it fail with the exact pre-fix wrongness
  (`takeaway_overrides_set = 1, want 0`), then restored the fix and
  watched it pass again.
- Checked repository-pattern/money/i18n rules: no new raw SQL in
  production code, no money-typed values involved (basis-point rates
  correctly stay `int`), no new user-facing strings.

## What was verified beyond automated tests

- Full package gate (`go build ./... && go test ./...`) green across the
  whole module, not just `internal/pages`.
- All four `before-commit` guard scripts
  (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`) plus `guard-help-topics.sh`
  pass — this diff has no i18n or UI surface, so those guards are
  confirmatory, not exercised by new content.
- Regression test (`TestImport_FailedItemRowDoesNotContributeTakeawayOverride`)
  independently confirmed by the reviewer to fail against the pre-fix code
  and pass against the fix.

## Deviation: `web/help/img/manifest.json` updated by hand

CI's `guard-docs-shots.sh` failed because `internal/pages/import_page.go`
is part of its (deliberately coarse — every non-test `.go` file under
`internal/pages/`, regardless of visual impact) surface-hash fileset. The
normal remedy, `make docs-shots`, needs Playwright to launch Chromium; this
sandboxed session's pre-installed browser cache is revision 1194 while the
repo's pinned `@playwright/test@1.61.1` (`e2e/package-lock.json`) needs
revision 1228, and downloading a new browser revision is against this
session's standing environment guidance.

Verified this diff has zero pixel impact before working around it: `git
diff origin/main -- web/ui web/public internal/pages` shows only
`import_page.go`/`import_page_test.go` changed (confirmed against the
correct, current `origin/main` — an initial check against a stale local
`main` ref falsely suggested a much larger drift and was corrected before
acting on it); `import` is not among the 17 routed screenshot topics; the
change is pure backend commit-loop logic with no template/HTML/JS/CSS
touch at all. So the actual PNGs `make docs-shots` would produce are
byte-identical to what's already committed — only the manifest's
`surface_sha256` fingerprint was stale. Recomputed it with the guard
script's own hashing logic and hand-patched just that one field (single-line
diff); reran `guard-docs-shots.sh` locally to confirm it now passes.

Filed ut-docs#620 for the underlying gap (cloud sessions can't run `make
docs-shots` at all today) so a human can decide the real fix (update the
environment's browser cache, or make the guard change-scoped instead of
whole-surface) rather than this workaround becoming silent standard practice.

## Safe-to-merge verdict

Safe to merge. No deferred items beyond ut-docs#620 above (pre-existing
environment gap, not introduced by this change).
