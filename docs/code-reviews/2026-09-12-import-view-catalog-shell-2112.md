# Review: Import dialog's "View catalog" stays in the /items shell (ut-docs#2112)

## What shipped

`POST /api/import`'s commit success summary ("View catalog") plain-navigated
the whole browser page to bare `/catalog` even when the import ran inside
the `/items` shell's dialog overlay (`#import-modal`, ut-docs#2095) — exactly
the railless standalone destination ut-docs#2090 moved every other in-shell
exit away from. The POST handler has no header of its own to tell "loaded
inside the dialog" from "loaded standalone" (an `hx-post` form submit
carries `HX-Request: true` either way, unlike the GET handler which already
has `.InItemsShell`), so the signal is threaded through as a hidden form
field (`in_items_shell`) that rides along on the POST the same way
`commit`/`staged_id` already do.

- `web/ui/pages/import.html`: both `#import-form` variants now carry
  `<input type="hidden" name="in_items_shell" value="{{ if .InItemsShell }}1{{ else }}0{{ end }}">`.
- `internal/pages/import_page.go`: the commit summary's "View catalog"
  control branches on `r.FormValue("in_items_shell") == "1"` — standalone
  keeps the unchanged `<a href="/catalog">`; in-shell renders a button that
  refreshes `#items-panel` (a plain `htmx.ajax` call) and then closes the
  dialog, both from the same `onclick`. The commit-summary `<div>` also
  gained a `data-import-committed="1"` marker (see F2 below).
- `internal/pages/import_stage.go`: `commitStagedImportForSetup`'s
  success sentinel now matches `data-import-committed="1"` instead of
  `href="/catalog"`.
- `web/help/en/catalog.md`: the "View catalog" sentence now describes both
  the in-shell (refresh-and-close) and standalone (navigate) outcomes
  instead of one blanket claim.
- `e2e/tests/catalog-import-view-catalog-shell-2112.spec.ts` (new): drives
  a real commit from inside the dialog and from the standalone page,
  asserting the correct control/behaviour in each.
- `internal/pages/import_page_test.go`: `TestImport_CommitInItemsShellClosesDialogNotBareNavigation`
  pins the handler's read of `in_items_shell` independent of template wiring.

## Independent review

Two Opus passes (fresh context, isolated worktree, different model from the
Sonnet session that implemented this — `complexity:medium` routing).

**First pass** reviewed the initial fix and found it real but incomplete:

- **F1 (major):** closing the dialog alone left `#items-panel` showing the
  pre-import catalog — the operator's "View catalog" tap would land on a
  stale list missing the rows they just imported, reintroducing the exact
  doubt ut-docs#1171 added this button to remove. Fixed by having the
  button's own click also refetch `#items-panel`.
- **F2 (medium):** `commitStagedImportForSetup`'s success sentinel matched
  `href="/catalog"`, which stopped being a reliable signal once the summary
  could also render as a button. Fixed with the `data-import-committed="1"`
  marker, present on the real commit-summary div regardless of which
  control it renders (verified: it's the only emitter, and
  `renderImportCurrencyConfirm`'s own div is a different code path that
  returns before this one is ever reached).
- **F3 (medium):** the manual's "takes you straight to your items" claim
  didn't hold for the in-shell path before F1 was fixed.

Also independently verified (first pass): the `.InItemsShell` /
`in_items_shell` mechanism holds through the ut-docs#970 currency-confirm
re-render (`hx-include="#import-form"` on the resubmit button includes the
outer form's hidden fields, confirmed by reading the vendored htmx source
directly rather than assuming); no i18n/XSS issue; the tax-codes `?` and
"Manage per-plugin overrides" links mentioned in the original issue as
lower-priority are confirmed out of scope (different template,
`tax_codes.html`, untouched here); TDD-verified by reverting the handler
hunk, confirming the new Go test fails with a clear message, then
restoring.

**Second, scoped pass** reviewed only the F1–F3 fix-of-fixes and found the
first attempt at F1 introduced two new problems, plus a docs-guard miss:

- **G1:** `make docs-shots` hadn't been re-run after the F1–F3 commit —
  `guard-docs-shots.sh` is CI-blocking. Fixed by regenerating.
- **G2 (medium, real UX regression):** the first F1 fix bound the refetch
  to `#import-modal`'s generic `close` event, which also fires on a plain
  cancel (the back-link) — wiping the operator's live search/category-filter
  state on the Catalog panel behind it for no reason. Verified live with a
  throwaway spec before and after the fix (search text and category filter
  now survive a cancel). Fixed by moving the refetch into the "View
  catalog" button's own `onclick` instead of a dialog-wide listener — the
  cancel path is untouched, and the earlier listener was removed from
  `catalog.html` entirely.
- **G3 (test quality):** the e2e test raced a `page.waitForResponse()`
  predicate loose enough to also match this same spec file's own `afterEach`
  cleanup navigation — on a genuine regression this stalled the test for the
  full 30s timeout and then failed on an unrelated later assertion instead
  of the real one. Fixed by dropping the race; Playwright's own
  `toBeVisible()` auto-retry is enough, and a reverted-fix run now fails
  fast, on the correct assertion, with a clear message (verified: reverting
  just the button's `onclick` change reliably fails the "row visible" line,
  not an earlier one).
- **G5 (medium):** the F3 rewording introduced a fabricated navigation
  path ("Import in the side menu" — no such menu entry exists anywhere in
  `internal/uislot/slot.go`'s registry) and an over-broad claim (the
  Catalog-page Import icon leads to the standalone page too, on a bare
  `/catalog` visit — the real distinction is shell-vs-standalone, not
  icon-vs-menu). Reworded again around that distinction, with no invented
  route.

Both reviews also ran the full `gofmt`/`go vet`/`go build`/`golangci-lint`/
guard set and the affected Go test suite; both came back clean throughout.

## Verified beyond automated tests

- Ran `e2e/tests/catalog-import-view-catalog-shell-2112.spec.ts` and
  `items-shell-catalog-import-taxcodes-dialog-2095.spec.ts` against a real
  Chromium instance (not just asserted rendered HTML) — both the in-dialog
  commit-and-refresh flow and the standalone-page flow, at every stage of
  the fix.
- Screenshotted the commit success state at 1024×600 and 360×740 — the
  button renders identically to the pre-existing `.btn.primary` treatment
  at both breakpoints, no overlap/clipping.
- Drove a throwaway spec confirming the cancel path (back-link, no import)
  leaves the Catalog panel's search text and category filter untouched,
  both before and after the G2 fix (regression reproduced, then fixed).
- `make docs-shots` regenerated for real (not skipped) after every round
  that touched `web/ui/**` or `internal/pages/**.go`; `guard-docs-shots.sh`
  green on the final diff.

## Explicitly deferred (out of scope for this card)

- The tax-codes `?` help link and "Manage per-plugin overrides →" link
  mentioned in the original issue as "same shape, lower priority" — these
  live in `tax_codes.html`, a different dialog, untouched by this diff.
  Worth their own card.
- `web/ui/pages/catalog.html`'s `#export-msg` id collision between
  `catalog.html` and `import.html` (the dialog's own Export button's
  `hx-target` resolves to the first, hidden, match) — pre-existing,
  introduced by ut-docs#2095, unrelated to this diff.

## Verdict

Safe to merge. Full gate green (`gofmt`, `go vet`, `go build ./...`,
`go test ./...`, `golangci-lint run ./...`, every CI-blocking guard) on the
final diff; two independent Opus reviews, all findings from both fixed and
re-verified; e2e-driven in a real browser, not just asserted HTML.
