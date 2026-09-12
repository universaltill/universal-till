# Review: Locations admin adopts the record_dialog/list_header pattern (ut-docs#2124)

## What shipped

`web/ui/pages/locations.html` + `internal/pages/locations_page.go` ported
from the old list-plus-inline-form admin pattern to the shared
`record_dialog`/`list_header` dialog (ut-docs#2010), mirroring the shipped
reference implementation `web/ui/pages/categories.html` +
`internal/pages/categories_page.go` exactly:

- `list_header` above the table (search + icon-only New), every row
  tap-to-edit (`data-record-open` + `data-field-*` prefill, a focusable
  pencil button as the keyboard path), one full-screen `record_dialog`.
- `redirectLocations`/`renderLocationsDialogError` mirror
  `redirectCategories`/`renderCategoryDialogError`: a successful
  hx-boosted mutation answers with `HX-Redirect` (a real browser
  navigation) instead of a bare 303 (which a boosted form's own fetch/XHR
  layer would otherwise follow itself, swapping the whole `/locations`
  page's HTML into the dialog's message region); a refusal renders inside
  the dialog's own `aria-live` message region instead of redirecting the
  whole page away from under the operator. Every refusal path (empty
  name, `replica_use_primary`, `in_use`, `last_location`) now routes
  through this pair. `isHtmxDialogRequest` is reused directly from
  `categories_page.go` (same Go package, package-private) — not
  redefined.
- No change to the actual business rules (`StockLocationInUse`,
  `CountActiveStockLocations<=1`, `requirePrimary`) — only how a refusal
  is *delivered* to the browser changed.
- New locale keys (`locations.search_placeholder`/`.new`/`.create`/
  `.edit`/`.deactivate_confirm`/`.empty`) added to every
  `web/locales/*.json`; now-orphaned `locations.new.title`/`.new.submit`/
  `.rename` removed.
- `web/help/en/inventory.md`'s "Stock locations" section rewritten to
  describe the add-icon/tap-to-edit-popup flow instead of the old
  inline-form-card/per-row-Rename-button flow.
- New Go-level tests (`locations_page_test.go`) covering the htmx dialog
  path: in-dialog refusal rendering, `HX-Redirect` on success, replica
  refusal in-dialog. New e2e spec
  (`e2e/tests/locations-record-dialog-2124.spec.ts`): New→create,
  row-tap→edit-prefill→rename, a refused mutation staying in-dialog,
  deactivate (behind the real `hx-confirm` browser dialog)/activate
  round-tripping, and geometry + status/lock/exit-to-OS reachability
  (coding-standards.md §10) at 1024×600 and 360px.

## Scoping note

Split out of ut-docs#2124's original four-destination ask (the product
owner re-raised Fiscal register/Locations/Registers/Country settings
together) during BA scoping, to align with the already-existing epic
ut-docs#2012's own "one card per screen" breakdown rather than
duplicating it. This card = Locations only. Registers → ut-docs#2185
(separate PR, reviewed separately). Fiscal register/Country settings →
ut-docs#2186/#2187, routed to Backlog — both are compliance-facing per
#2012's own note and need their own judgement pass before a mechanical
port is safe, not built here.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (`complexity:easy`
routing).

**Verdict: safe to merge, no blocker or should-fix findings.**

Verified for real, not just read: `gofmt`/`go build`/`go vet` clean;
`golangci-lint run ./...` (full repo) 0 issues; `go test
./internal/pages/... -v` 17/17 pass including the 3 new ones; every
listed guard (`guard-i18n`, `guard-docs-shots`, `guard-help-topics`,
`guard-help-drift`) green.

**TDD re-verified independently** (own worktree, reverted only
`locations_page.go` to `main`, kept the new tests): all 3 new tests fail
with the real pre-fix error (`code=303` where 400/200 was expected — the
handler executed the old bare redirect, not a template/response bug);
restored the fix, all 3 pass. Matches this session's own earlier
independent TDD check (a separate worktree, same method) byte for byte.

**e2e spec soundness checked by execution, not inspection**: installed a
version-matched Playwright locally and ran
`locations-record-dialog-2124.spec.ts` against both the fixed and the
reverted handler. Fixed: 5/5 pass. Reverted: 2/5 genuinely fail — a real
Playwright strict-mode violation (`locator('#location-dialog') resolved
to 2 elements`), because the old handler's bare 303 gets followed by
hx-boost's own fetch layer and the entire `/locations` page HTML gets
swapped into `#location-dialog-msg`, injecting a second `#location-dialog`
element into the DOM. Confirms the spec cannot pass against the pre-fix
code.

**Line-by-line check**: `redirectLocations` is behaviourally identical to
`redirectCategories`; `renderLocationsDialogError` correctly omits the
`%d`/`errCount` interpolation branch `renderCategoryDialogError` needs
(independently confirmed no `locations.error.*` value contains a
placeholder); `isHtmxDialogRequest` is not redefined (confirmed by grep
and by the fact that a duplicate symbol would be a compile error, and the
build is clean).

**Locale/help-topic checks**: the locale diff across all four files is
scoped exactly to `locations.*` keys, same shape in each, no unrelated
key reordering. `inventory.md`'s new prose correctly names the Save
button and the add-icon/tap-to-edit flow, matching what
`record_dialog.html` actually renders.

**One nit, not fixed, not blocking** (left for a future pass, not this
one): the e2e spec's "refused mutation" test asserts
`[data-record-dialog-msg]` is merely non-empty rather than
content-specific (`categories-record-dialog-2010.spec.ts`'s own
equivalent test asserts `toContainText(/active item/i)`). The rest of
that same test's assertions (dialog still visible, exact URL match) are
what actually catch the regression, so this is cosmetic.

No secrets, no real client/shop name in any test/demo data (all
`E2E ...`/`Loading Bay`/`Satellite Pop-up`-style placeholders). No
file-write path in this diff (N/A for the `os.MkdirAll`/`paths.Data`
check). No new CSS/inline styles — RTL and design-token reuse inherited
for free from the unmodified shared partials.

## Safe-to-merge verdict

Yes. Merged via `merge_method: "merge"` (never squash/rebase — ut-docs#250).
