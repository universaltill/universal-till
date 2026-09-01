# Code review — bulk "backfill barcodes from SKU" (catalog)

- **Date:** 2026-09-01
- **Branch:** `feat/1356-catalog-barcode-backfill-from-sku`
- **Reviewer:** independent review, `opus` subagent, `isolation: "worktree"`
  (`complexity:medium` per the model-routing table)
- **Verdict:** Safe to merge after fixes. Fixed and re-verified.

## Context

ut-docs#1356: the product owner found a live catalog whose items were
imported barcode-less (before/without ut-docs#1224's import-time opt-in),
with no way to backfill barcodes for an already-imported catalog short of
typing one into every item's edit screen by hand. Ships:

- A bulk "backfill barcodes from SKU" action on the Catalog page, reusing
  #1224's exact derivation (`catimport.DeriveNumberBarcode`, exported for
  this).
- Preview-before-apply (dry run, then a separate commit) since this
  mutates a live catalog.
- A per-shop Settings toggle for whether the import-time checkbox (#1224)
  starts pre-ticked by default, per the ticket's "settings-not-a-decision"
  resolution rather than silently flipping #1224's explicit default-off.

## What shipped

- `internal/catimport/catimport.go` — exported `deriveNumberBarcode` as
  `DeriveNumberBarcode` for reuse (ADR-0059 §3: call the same function,
  don't reimplement it).
- `internal/data/catalog_repo.go` — `ItemsWithoutBarcode` (candidate query)
  and `BarcodeOwner` (non-transactional dry-run lookup, deliberately
  separate from `AddBarcode`'s own transactional `ensureBarcodeAvailable`).
- `internal/pages/catalog/handlers.go` — `GET`/`POST /api/catalog/barcode-backfill`
  (preview / commit), sharing `computeBarcodeBackfillPlan`.
- `internal/pages/import_page.go` + `internal/pages/settings_page.go` +
  `internal/data/barcode_settings.go` — the shop-default pre-tick setting
  and its `/api/settings/catalog-import-barcode-default` endpoint.
- `web/ui/partials/catalog_barcode_backfill.html`, `web/ui/pages/catalog.html`,
  `web/ui/pages/settings.html` — the dialog and the new settings card.
- i18n: `web/locales/{ar,en,fa,tr}.json`. Manual:
  `web/help/{ar,en,fa,tr}/catalog.md` + regenerated screenshots.
- Tests: `internal/data/catalog_repo_barcode_backfill_test.go`,
  `internal/pages/catalog/barcode_backfill_test.go`,
  `internal/pages/import_barcode_optin_test.go`,
  `internal/pages/settings_page_test.go`, and
  `e2e/tests/catalog-barcode-backfill-1356.spec.ts`.

## Independent review findings (opus, worktree-isolated)

1. **BLOCKER — `HX-Refresh: true` discarded the commit result.** The
   commit handler set `HX-Refresh: true` *and* rendered the result
   fragment in the same response. htmx processes `HX-Refresh` before the
   swap (`location.reload(); return`, vendored `htmx.min.js`), so the page
   reloaded and the "Assigned N, skipped: ..." report was never inserted
   into the DOM — a partial backfill (some SKUs skipped) was
   indistinguishable from a complete one, defeating the ticket's own
   "report any that can't" requirement. The codebase already knew this
   shape of bug (`pending_pairings_test.go:205`'s "must not set HX-Refresh
   on a failed approve" guard) but nothing connected the two. The e2e spec
   even documented the reload in a comment without noticing the report was
   unreachable underneath it.
   **Fixed:** dropped `HX-Refresh`; the result fragment now renders as an
   ordinary swap, and its own "Close" button closes the dialog *then*
   reloads (`location.reload()`), so the operator reads the report first —
   same close-then-reload shape as `plugin_install_modal.html`.
2. **Real-but-minor — intra-batch derived-code collision.** Two different
   SKUs in the same candidate set can derive the same code (EAN13
   weight/price-prefix variants collapse to one `LookupKey`; a trailing
   `.0` spreadsheet artifact is stripped). `BarcodeOwner` only sees data
   committed *before* the plan ran, so both rows showed as eligible in the
   preview though only the first could actually get the code — `AddBarcode`'s
   own transaction prevented any data corruption (first wins, no double
   write), but the preview's count and commit's actual result silently
   diverged, and the skip reason named an item that itself had no barcode
   moments earlier. Confirmed empirically by the reviewer before the fix.
   **Fixed:** `computeBarcodeBackfillPlan` now tracks codes claimed earlier
   in the same batch and moves the later duplicate into the "already used"
   bucket up front, naming the batch-claiming item — preview and commit now
   always agree. Regression test added
   (`TestBarcodeBackfillPlan_IntraBatchCollisionOnlyClaimsOnce`).
3. **Real-but-minor — new Settings card had no heading.** The only
   headingless card on the page; read as part of "Barcode types" directly
   above it, and all 4 manual locales already (wrongly) pointed at that
   card by name. **Fixed:** added its own `<h2>` (+ i18n key in all 4
   locales) and corrected the manual's cross-reference in all 4 locales.
4. **Nitpick — malformed Turkish string.** `tr.json`'s hint had a stray
   `-sınız` landing outside its parenthetical. **Fixed.**
5. **Nitpick — dialog opens even on a failed preview request.** Pre-existing
   convention shared with neighbouring dialogs, not a regression introduced
   here. **Not fixed** — out of this ticket's scope.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...` / `go vet ./...` | pass |
| `go test ./internal/catimport/... ./internal/data/... ./internal/pages/... ./internal/pages/catalog/... ./internal/pages/common/...` | pass |
| `go test ./...` (full suite) | pass |
| All CI-blocking guards (`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`, `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-e2e-fixtures-import`, `check-brand-assets`, `guard-makefile-version`) | all pass |
| `make docs-shots` | 92/92 screenshots regenerated (settings page's new heading changed its rendered surface) |
| `e2e/tests/catalog-barcode-backfill-1356.spec.ts` against a real running till + Chromium (`/opt/pw-browsers/chromium-1194`) | pass — confirms the HX-Refresh fix works end-to-end, not just in the Go unit tests |

**TDD claim re-verified independently** (by the reviewer, inside its own
isolated worktree, on the most bug-shaped claim — `import_page.go`'s
"only applies on first render, never on commit/wizard-preview" gating):
reverted the gate to unconditional, confirmed
`TestImport_ShopDefaultOn_ExplicitUntickOnCommit_NeverAppliesBarcodes`
failed with a real assertion (`barcode rows for sku=30005 = 1, want 0`),
restored, confirmed green again. Test quality assessed as genuinely
exercising the interesting cases (collision, no-symbology-match,
already-has-barcode exclusion, the settings-default pre-tick gating,
preview/commit re-derivation), not tautological.

## Scope note

No money/quantity types touched. Repository pattern respected — all new
SQL lives in `internal/data/catalog_repo.go`; `internal/pages/**` reads
through the repo methods only. No new file writes, no cwd-relative paths.
Elevation on the new settings endpoint matches its neighbours exactly.
