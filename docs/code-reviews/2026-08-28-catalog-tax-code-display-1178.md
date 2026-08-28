# 2026-08-28 — Catalog tax code shown by name, not raw id (ut-docs#1178)

## Summary

The catalog page's TAX column and the item-edit "Tax code" field both
rendered the raw `tax_code_id` (a UUID in production, e.g.
`4ca66fd2-8379-4f6b-90a7-63c959d0e44b`) instead of the tax code's name —
looking like broken data to a shop owner checking their VAT setup. Fix:
resolve the id to its name via a new `taxCodeName` template func, and
convert the item-edit field from a free-text/datalist input into a proper
`<select>`.

Independent review (Opus, isolated worktree, `fix/1178-catalog-tax-code-
display` at WIP commit `642b9af`) found one **blocking** regression the
initial diff introduced — fixed in this same branch before merge — plus
two non-blocking notes, both addressed or accepted below.

## Change (as committed)

- `internal/httpx/httpx.go`: `baseFuncs` gets a default
  `"taxCodeName": func(*string) string { return "" }` — a safe fallback so
  any renderer that pulls in `catalog_table.html` without caring about tax
  codes (e.g. `sync_banner_test.go`'s minimal `NewRenderer` calls) still
  parses; `html/template` requires every function a template text
  references to be registered at Parse time, whether or not that branch
  executes.
- `internal/pages/catalog/handlers.go`:
  - `taxCodeNameFunc([]data.TaxCodeView) func(*string) string` — resolves
    an item's `TaxCodeID` to its name, wired into all three places that
    render `catalog_table.html` (the full `/catalog` page, `renderCatalogTable`
    after every mutation, `renderVariantsPanel`'s out-of-band table
    re-render).
  - `listLookups` no longer fetches tax codes (only categories/brands now)
    — the tax-code list is fetched separately via `repo.ListAllTaxCodes`
    (see blocking finding below for why "all", not just active).
- `web/ui/partials/catalog_table.html`: TAX column resolves the name via
  `taxCodeName .TaxCodeID`, falling back to "—".
- `web/ui/pages/catalog.html`: item-edit Tax code field is now
  `<select name="taxCode">`, options from `.TaxCodes`, a blank "— none —"
  first option, inactive codes suffixed "(inactive)" but still selectable.
- `web/ui/partials/catalog_lookups.html`: removed the now-dead
  `taxcodes-list` datalist (nothing references it after the `<select>`
  conversion).
- `web/locales/{en,ar,fa,tr}.json`: two new keys,
  `catalog.tax_code.none` and `catalog.tax_code.inactive`.
- `web/help/img/{en,ar,fa,tr}/catalog.png` + `manifest.json` regenerated
  (`make docs-shots`, scoped to the `catalog` topic) — the demo catalog's
  TAX column visibly changed (raw ids → "Standard VAT"/"Zero-rated" etc.).
  `web/help/en/catalog.md`'s prose was checked and needs no change — it
  only mentions tax in the import/export context, never describes the
  item-editor's widget shape.
- New test `internal/pages/catalog/tax_code_display_test.go`:
  `TestCatalogPage_TaxCodeShowsNameNotID`, `TestCatalogTablePartial_
  TaxCodeShowsNameNotID`, and `TestCatalogPage_InactiveTaxCodeSurvivesUnrelatedSave`
  (the regression guard, added after the review finding below).

## Independent review findings

### BLOCKING — F1: a deactivated tax code was silently wiped on save (fixed)

The first version of this diff built the `<select>`'s options from
`ReadLookup(ctx, "tax_codes")`, which filters `WHERE is_active = 1` — the
same filter the old datalist used. That was fine for a **text input**
(its `.value` holds whatever string it's given, regardless of whether a
datalist option matches), but a `<select>` behaves differently: per the
HTML spec, setting `.value` to a string matching no `<option>` leaves
`selectedIndex = -1`, and an unselected `<select>` contributes **nothing**
to the submitted form. `parseItemInput`'s `strPtr(r.Form.Get("taxCode"))`
then reads `""` → `nil`, and `CatalogRepo.UpdateItem` writes
`tax_code_id = NULL`. Net effect: renaming (or any other edit of) an item
still assigned a since-deactivated tax code silently cleared its VAT
assignment, HTTP 200, no warning — a real regression the old widget never
had, verified live by the reviewer with a throwaway probe before the fix
landed.

**Fix**: the item-edit `<select>` and the TAX-column name lookup both now
draw from `repo.ListAllTaxCodes` (active + inactive) instead of the
active-only lookup — a retired code stays selectable (labeled
"(inactive)") and its name still resolves instead of falling back to "—".
`ListTaxCodes` (the strictly-active method) itself is untouched, per its
own doc comment, which still governs at least one other caller.

Guarded by the new `TestCatalogPage_InactiveTaxCodeSurvivesUnrelatedSave`
— TDD-verified personally: reverted `handlers.go`/`catalog.html` to the
pre-fix (active-only) version via `git stash`, confirmed the test fails
red (`--- FAIL`, no matching `<option>`, tax code cleared on save), then
restored and confirmed green. Also re-verified live in a running instance
(throwaway till, real browser): created a tax code, deactivated it,
assigned it to an item, renamed the item, reloaded, confirmed the select
still shows "Old Reduced Rate (inactive)" selected — screenshot taken,
matches other fields' styling.

### NON-BLOCKING — F2: a doc-comment overstated why `*string` is needed (fixed)

The first version's comment claimed html/template never auto-dereferences
a pointer for a template func argument. The reviewer tested this
directly: a **non-nil** `*string` *does* auto-deref into a `func(string)
string`; only a **nil** one panics ("dereference of nil pointer of type
string"). Comment corrected to state the real reason: items with no tax
code (a `nil` `*string`) are the common case, and a `func(string) string`
signature would abort the whole page render on the first such item.

### NON-BLOCKING — F3: the `httpx.baseFuncs` no-op default fails silently (accepted, not fixed)

`internal/httpx/httpx.go`'s default `taxCodeName` returns `""` rather than
erroring if some future renderer forgets to override it — a silent "—"
instead of a loud failure, and a minor layering smell (generic `httpx`
now knows a tax-specific func name exists). Accepted as-is: all three
production render sites override it correctly, the only other renderer is
a test with no `Items` to iterate, and a louder failure mode (panic on
missing override) would make `catalog_table.html` unusable from any future
minimal test harness the way `sync_banner_test.go` already is — the
lesser risk.

### NON-BLOCKING — F4: two wasted queries per mutation (resolved as a side effect of the F1 fix)

`listLookups` used to run three queries (categories, brands, tax_codes)
at each of the two mutation-render sites but only needed two of them. The
F1 fix's refactor — `listLookups` now returns only categories/brands, tax
codes fetched once via the dedicated `ListAllTaxCodes` — removes the
redundant query as a byproduct.

### NON-BLOCKING — F5: item-edit form now has two different widget idioms (accepted, follow-up filed)

Category and Brand remain free-text-with-datalist and still show a raw id
once populated by the row-click JS — same underlying bug class as the
original #1178 report, deliberately out of scope here (the card only
named `tax_code_id`). Confirmed the diff doesn't touch either. Filed as
universaltill/ut-docs#1212 — noting explicitly that naively repeating the
`<select>` conversion for category/brand would reproduce F1, since
`ReadLookup` filters those by `is_active` too.

## Verification performed independently (by the reviewer, in an isolated worktree)

- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.
- `go test ./...` — full repo suite, 41 packages, all pass (re-run after
  the F1 fix landed: still all pass, catalog package covered above).
- `bash scripts/ci/guard-data-access.sh` — pass, no SQL added outside
  `internal/data`.
- `bash scripts/ci/guard-i18n.sh` — pass, 1301 keys resolve, all 4 locales
  match `en.json`.
- `bash scripts/ci/guard-help-topics.sh` — pass.
- `bash scripts/ci/guard-docs-shots.sh` — pass, 23 topics × 4 locales
  fresh.
- `bash scripts/ci/guard-compliance-claims.sh`, `guard-kiosk-engine.sh`
  (extra, not required for this diff's surface) — pass.
- Repo-wide grep for a fourth `catalog_table.html`/`catalog.html` renderer
  the diff might have missed: none found beyond the three wired sites and
  the `httpx` test (covered by the base default).
- XSS/escaping: clean — `{{ .ID }}` sits in attribute-value context,
  `{{ .Name }}`/`{{ $name }}` in text context, both plain `string`
  (not `template.HTML`), so `html/template`'s contextual auto-escaping
  applies with no bypass.
- Money: N/A, confirmed — no amount-shaped code touched.
- File-write bug classes (`paths.Data` without `MkdirAll`, cwd-relative
  path): N/A, confirmed — this diff performs no file I/O.
- Manual (`web/help/en/catalog.md`): confirmed no prose change needed;
  screenshots regenerated and fresh per `guard-docs-shots.sh`.

### TDD re-verification (not taken on trust)

Both original tests (`TestCatalogPage_TaxCodeShowsNameNotID`,
`TestCatalogTablePartial_TaxCodeShowsNameNotID`) were reverted-and-restored
by the reviewer against the *original* (pre-F1-fix) production files:
failed with the raw UUID visibly present in the dumped response body
(`<td>4ca66fd2-8379-4f6b-90a7-63c959d0e44b</td>`), then passed again after
restoring. The F1 regression test
(`TestCatalogPage_InactiveTaxCodeSurvivesUnrelatedSave`) was TDD-verified
personally after the review (see F1 above) the same way.

## Verdict

**Safe to merge** — the blocking finding (F1) is fixed and guarded by a
regression test, independently re-verified failing-then-passing. Non-
blocking findings are either fixed (F2), a resolved side effect (F4), or
explicitly accepted with reasoning (F3), and the deliberately out-of-scope
one (F5) is filed as a follow-up card rather than silently dropped.

## Deferred / follow-up

- universaltill/ut-docs#1212 — apply the same "show the name, not the raw
  id" treatment to the item-edit Category and Brand fields (same bug
  family as the original #1178 report), with the F1 lesson (a `<select>`
  needs the *assigned* value present as an option, even if retired) baked
  in from the start.
