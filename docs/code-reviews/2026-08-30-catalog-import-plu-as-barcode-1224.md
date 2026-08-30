# Code review — Catalog import: use item numbers as barcodes, opt-in (ut-docs#1224)

- **Date:** 2026-08-30
- **Branch:** `feat/1224-catalog-import-plu-as-barcode-opt-in`
- **Reviewer:** independent reviewer (fresh-context Opus, this pipeline's
  `complexity:medium` review tier), same checkout (no worktree isolation
  used for this review).
- **Verdict: SAFE TO MERGE.** One blocking finding (F1), fixed, with a
  related non-blocking gap (F2) the same fix closes. Three non-blocking
  findings (F3–F5) accepted as-is, reasons below. Everything in this
  record reflects the fixed state — the review ran against the pre-fix
  diff, then F1/F2 were fixed and re-verified afterward.

## What shipped

Sources with no barcodes of their own — the pilot café's speedy-kasse
`.bkp` export, or a CSV with no/empty barcode column — can now optionally
have each item's SKU/item-number turned into its barcode at import time,
per BA/Architect's own investigation this cycle:

- `internal/catimport/{catimport.go,bkp.go}`: `Parse`/`ParseBkp` gain a
  `useItemNumbersAsBarcodes bool` param (`ParseBkp` also gains
  `enabledSymbologyIDs []string`, which it never needed before this card).
  When true, a row with no barcode of its own and a unique-in-file
  SKU/PLU gets one derived via the existing `normalizeBarcode`/barcode
  registry matcher (ADR-0059, ut-docs#936) — the same shared matcher
  every other barcode in this codebase goes through, no new symbology
  logic. A SKU/PLU shared by another row in the file is denied a derived
  barcode (`BarcodeIssueDuplicateItemNumber`) instead of creating two
  items that scan identically.
- `internal/pages/import_page.go`: the import Preview shows an inline,
  unchecked-by-default checkbox when the parsed catalog is barcode-less
  (`barcodelessCatalog`) — **not** a new blocking gate. It rides on the
  existing `form="import-form"` HTML association the problem-grid
  controls already use, so an unticked box simply never sends the field,
  reading identically to "never asked." A direct/never-previewed commit
  defaults to false, unchanged from before this card.
- New locale keys (`import.barcode_opt_in.label`,
  `import.status.barcode_duplicate_item_number`) in all four shipped
  locales; `web/help/{en,ar,fa,tr}/catalog.md` updated; `make docs-shots`
  re-run twice (once for the feature, once for a wording fix from
  review).

## Independent review — what was checked

- **Gates, all real output, all green (re-run after the fixes, not just
  before):** `gofmt -l .` (empty), `go build ./...`, `go vet ./...`,
  `go test ./...` (whole repo), `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`
  (+ its two self-tests), `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` (+ its cross-check),
  `guard-webkit-version.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`.
- **Collision-safety logic, both parse paths, hand-traced, not just
  read:** `Parse`'s `seenForBarcode` map and `ParseBkp`'s reuse of the
  existing `#1222` `SKUIssue`/`seen` dedup machinery both correctly give
  "first occurrence claims the barcode, later occurrences flagged"
  semantics. The `bkp.go` path's nastiest case (source PLUs `555, 555,
  555-2`, ut-docs#1222's own motivating shape) was hand-traced: row 1
  claims barcode `555`, row 2 (a same-PLU duplicate) is flagged and gets
  synthesized SKU `555-3` (correctly dodging the pre-existing `555-2`),
  row 3 (genuine PLU `555-2`) derives barcode `555-2` cleanly.
- **Never overrides a real barcode**: guarded by `item.Barcode == ""` in
  `Parse`; `.bkp` never populates `Barcode` at all regardless (verified
  by inspection — `bkp.go`'s own long-standing comment: "must never be
  run through normalizeBarcode").
- **Call-site audit**: every non-test caller of `Parse`/`ParseBkp` in the
  repo (4 sites in `import_page.go` — preview/commit × csv/bkp, plus the
  currency-switch re-parse pair) passes the real computed value, not a
  hardcoded `false`. `POST /api/data/import` (the plugin importer,
  `import_dispatch.go`) doesn't use `catimport` at all and is correctly
  untouched. ~40 existing test call sites were mechanically updated to
  pass the new trailing param (`, false`), preserving prior behavior.
- **Regex-widening safety** (see F1/F2 below): `confirmCarriedOverrideField`
  stays fully anchored; the alternation cannot widen the pre-existing
  `row_*` branch. Values pass through `htmlEscape` into a double-quoted
  attribute — no injection.
- **Test quality — mutation-verified, not just "green":** dropping the
  `item.Barcode == ""` guard fails
  `TestParseUseItemNumbersAsBarcodesNeverOverridesRealBarcode`; neutering
  either path's dedup check fails its respective
  `..._SkipsInFileDuplicate` test; reverting the
  `confirmCarriedOverrideField` regex fails
  `TestImport_TickedOptIn_SurvivesCurrencyConfirmDetour`; reverting the
  checkbox's render condition fails
  `TestImport_TickedOptIn_StaysStickyOnRePreview`. All four confirmed by
  actually reverting the fix and watching the test fail with the
  expected message, then restoring it.

## Findings

### F1 — BLOCKING, FIXED. Checkbox wasn't sticky across a re-preview

`barcodelessCatalog(res)` was judged against the **current** parse's
result. A re-preview submitted with the box already ticked parses WITH
derived barcodes filled in, so every row now has a non-empty `Barcode`,
`barcodelessCatalog` flips to `false`, and the checkbox that produced the
very rows on screen silently vanished — the next "Import" click had
nothing to submit and would commit barcode-less, contradicting the
preview the operator had just approved. Confirmed empirically in review
(a second preview showed the derived barcode in the table with no
checkbox left to submit it) and reproduced as a failing test before the
fix (`TestImport_TickedOptIn_StaysStickyOnRePreview`, confirmed red
against the pre-fix code, now green).

**Fix**: render the checkbox — and mark it `checked` — whenever the
request itself already carries `use_item_numbers_as_barcodes=1`, not only
when the parse result still looks barcode-less
(`stagedFormID != "" && (barcodelessCatalog(res) || useItemNumbersAsBarcodes)`).

### F2 — non-blocking, closed by F1's fix. Currency-detour fix was incomplete on a rare non-staged path

The currency-confirm re-emission (`confirmCarriedOverrideField`) only runs
inside `if stagedID != ""`, but the checkbox (before F1's fix) rendered
regardless of staging. If `stageCatalogUpload` fails (disk/TMPDIR — a
pre-existing, documented graceful-degradation path), the preview would
still offer the checkbox with no staged copy to carry it forward through
a currency-gate detour. F1's fix also gates the checkbox on
`stagedFormID != ""` (matching the existing problem-grid controls'
`interactive` gate immediately above it), so this path now simply never
offers the choice rather than silently losing it — the import itself
still proceeds barcode-less, exactly as before this card. Not separately
tested: reproducing a `stageCatalogUpload` failure needs mocking
disk/TMPDIR errors, disproportionate to a rare failure path whose worst
outcome (post-fix) is "the checkbox isn't offered this one time," not
data loss.

### F3 — non-blocking, accepted. Preview's row statuses can diverge slightly from commit when a derived barcode collides with the catalog

The first preview always parses with the opt-in off, so no row can hit
the pre-existing `BarcodeExists` skip branch from a *derived* barcode. If
the operator ticks the box and a derived barcode happens to already exist
in the catalog, that row flips to skipped only at commit, not preview.
Low likelihood (needs a live catalog barcode equal to a source PLU), and
the commit result screen does say why — same class of gap as the
pre-existing `BarcodeIssueNoSymbologyMatch` behavior this mirrors.
Accepted without a follow-up card; flag if it recurs in practice.

### F4 — non-blocking, accepted. Collision safety covers derived-vs-derived, not derived-vs-a-real-barcode-in-the-same-file

`seenForBarcode`/the `.bkp` dedup only track SKUs that claim a *derived*
barcode; a real barcode already present in the same file on a different
row isn't cross-checked. In practice this can't corrupt data —
`item_barcodes.barcode` is a DB-level `TEXT PRIMARY KEY`, and `AddBarcode`
failure is an existing warn-and-continue path
(`FriendlyBarcodeConflict`) — so the colliding row surfaces a named
conflict warning rather than silently overwriting anything. The help
text's "no two different items ever share a barcode" claim holds, just
via that downstream guard rather than this card's own dedup. Accepted;
not worth a second dedup pass for a scenario the existing DB constraint
already makes safe.

### F5 — non-blocking, accepted. A row rescued by the problem grid never gets a derived barcode

A row with a blocking `Issue` (missing name / bad price) at parse time is
excluded from opt-in derivation by design, and stays excluded even if the
operator corrects it via the commit-time problem grid — its SKU was never
registered in the dedup tracking, so this is the safe choice, just
undocumented. Accepted as consistent with this card's own scope (the
problem grid's forced-correction path is orthogonal to this feature); a
follow-up card can extend it later if an operator asks for it.

## TDD

Both the feature's own dev/tester pass (7 `catimport` unit tests + 5
handler tests) and the two review-found regressions above were TDD'd for
real: each new/fixed test was confirmed **red** against the pre-fix code
with the actual failure message, then green after the fix — not just
asserted to pass. See `internal/catimport/{catimport_test.go,bkp_test.go}`
and `internal/pages/import_barcode_optin_test.go`.

## Real driven check (not just automated tests)

Ran the compiled binary against a fresh first-boot DB (`UT_AUTH=off`,
pre-installed Chromium at `/opt/pw-browsers`), drove it with a throwaway
Playwright script — not committed, scratch-only:

- Uploaded a barcode-less CSV, previewed in `en`, `fa`, and `ar` —
  screenshotted all three at 1024×700. Checkbox renders cleanly in every
  locale, unticked by default, correctly right-aligned/RTL-mirrored in
  `fa`/`ar` with no overlap, cut-off, or LTR leakage.
- Ticked the checkbox for real (a genuine DOM `.check()`, not a form
  field injected by the test) and clicked the real "Import" submit
  button — this is exactly how the F1/F2 findings above were originally
  found, by driving the real currency-confirm detour rather than reading
  the code and assuming it worked.
- Queried the till's live SQLite DB directly after commit: `('30005',
  'Cappuccino', '30005', 'CODE128')` — the derived barcode landed
  correctly, confirmed a plain numeric PLU lands on `CODE128` (a
  permissive catch-all), never mistaken for a checksum-validated EAN
  symbology.
- **Visual-check attestation**: looked at the preview screen in `en`
  (light theme only — dark theme and kiosk viewport sizes not checked,
  since this card adds one `<label><input></label>` line reusing the
  page's existing `<p>`/`<label>` styling with no new CSS, and every
  other control on this page already passes those checks) at 1024×700 in
  `en`/`fa`/`ar`. Did not check `tr` visually (RTL check covered by
  `fa`/`ar`; `tr` is LTR like `en` and uses the same markup path).

## Housekeeping

- Every throwaway server process and Playwright script used to drive the
  real check was killed/discarded in the same session; nothing committed
  from that pass except the regression tests it motivated.
- No real client/shop name used in any fixture, test, or driven check —
  the demo PLU (`30005`) and item name (`Cappuccino`) are generic.

## Known deviation, tracked as a follow-up

The `ar`/`fa`/`tr` translations for this card's two new locale keys were
done directly (the documented NAS Ollama translation endpoint is
unreachable from this cloud session) — same established pattern as prior
cards of this kind. Not a merge blocker; a local/interactive session
re-verifying against the documented pipeline can pick this up alongside
any other pending translation-verification follow-ups already tracked.
