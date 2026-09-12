# Admin tree: gate hx-get on fragment-capable rows only (ut-docs#2139)

## What shipped

`web/ui/partials/admin_tree.html` (ut-docs#2116, merged as
universaltill/universal-till#1094) wires `hx-get`/`hx-target="#admin-panel"`/
`hx-push-url` onto **every** row, unconditionally. `internal/pages/
admin_page.go`'s `adminGroupsFor` also renders an "Other" catch-all cluster
for any entry a `layout` plugin regroups INTO `menu.group.administration`
(ADR-0088 Decision F — a supported, validated, already-tested amendment).
That regrouped entry's own route has no `writeAdminTreeOOB`/
`httpx.IsFragmentSwap`-wired handler — only the six fixed `adminGroupOrder`
destinations do. Tapping (or `hx-get`-ing) such a row swapped that page's
whole standalone HTML document into `#admin-panel` — a second nav rail, a
second status bar, duplicate DOM ids, and the tree's highlight never
following the click.

Not reachable on any shipped till today (confirmed via `grep -rn
"menu.group.administration" plugins/` — no plugin uses the regroup
capability yet), but a real, reproducible defect in a path this repo
already built and tests for
(`TestAdminPage_RegroupIntoAdministrationMovesEntryFromMenuToAdminTree`),
and one any future `layout` plugin (built-in or third-party) hits the
moment it uses that fully-documented amendment.

## Fix

- `adminGroup.Entries` is now `[]adminTreeEntry` (a `uislot.Entry` plus a
  `FragmentCapable bool`), computed via a lookup (`adminFragmentCapableHrefs`)
  derived from `adminGroupOrder`'s own key set — so the fragment-capable
  set and the six real destinations can never drift apart.
- `admin_tree.html` only emits `hx-get`/`hx-target`/`hx-push-url` when
  `.FragmentCapable` is true; a non-fragment-capable ("Other"-cluster) row
  stays a plain `<a href>` real navigation, exactly as every row behaved
  before ut-docs#2116.
- New regression test, `TestAdminPage_OtherClusterRowIsPlainNavigationNot-
  FragmentSwap` (`internal/pages/admin_page_test.go`), reusing the same
  `installLayoutAmendments` regroup fixture as the existing "Other"
  catch-all test right above it.

## How this was found

Built and independently reviewed (Opus) as part of the original
ut-docs#2116 work, in parallel with a concurrent session that merged
universaltill/universal-till#1094 first — my own implementation was
superseded before I pushed. On standing down (per `scrum-master`'s Lane
ownership rule 7a), I checked whether the merged PR carried the same
class of bug my own independent review had caught, since neither
implementation's description mentioned it: `git show origin/main:web/ui/
partials/admin_tree.html` and `internal/pages/admin_page.go` confirmed the
merged code has the identical unconditional `hx-get` wiring and the same
un-flagged "Other" catch-all. Filed as ut-docs#2139 rather than silently
dropped, since the fix was already built, tested and verified.

## Independent review (fresh-context Sonnet — complexity:easy)

Ran the full gate set fresh (`gofmt`, `go build ./...`, `go vet ./...`,
`go test ./internal/pages/... ./internal/ui/...`, `golangci-lint run
./...`) — all clean. Re-verified the regression test via a real revert
(unconditional `hx-get`) → confirmed `TestAdminPage_OtherClusterRowIsPlain-
NavigationNotFragmentSwap` fails with the actual `hx-get="/users"` markup
in the message, then restored and confirmed it passes again, `git diff
--stat` empty. Confirmed `adminFragmentCapableHrefs` is genuinely derived
from `adminGroupOrder` (no hand-maintained second list), and every
existing `.Entries`/`adminGroup` call site still compiles and behaves
correctly through the new `adminTreeEntry` embedding. The narrow-width JS
fallback in `admin.html` is unaffected (its selector still matches every
row via `.items-row`, and stopping propagation on a row with no `hx-get`
to intercept is a no-op).

**Found the fix was incomplete for the bug class it claims to close**:
`registerAdmin`'s own default-panel-embed logic
(`current := groups[0].Entries[0].Href` → `embedAdminSection(mux, r,
current)`) was not updated to consult `.FragmentCapable`, unlike
`admin_tree.html`'s row wiring. When a `layout` plugin regroups an
always-visible core entry (`VisibleIf==""`, not `InNav` — e.g.
`/open-orders`) into Administration, that entry can become
`groups[0].Entries[0]` for a viewer (even a bare cashier) with nothing
visible in the three named clusters, and `embedAdminSection` embeds that
destination's entire standalone document into `#admin-panel` on a bare
`GET /admin` — reproduced concretely by the reviewer with a temporary
test: a cashier role got 200 with `#admin-panel` containing a literal
nested `<!DOCTYPE html><html>...` document. This is the same defect class
ut-docs#2139 exists to fix, reached via the untouched arrival path rather
than the fixed click path.

**Fixed**: added `firstFragmentCapableHref(groups)`, which walks the
groups in order and returns the first `FragmentCapable` entry's `Href` (or
`""` if none is) — `registerAdmin` now uses this instead of
`groups[0].Entries[0].Href` unconditionally, and skips setting `PanelHTML`
entirely when nothing visible is fragment-capable (leaving `#admin-panel`
empty rather than nesting a non-fragment-capable destination's whole
page). New tests: `TestFirstFragmentCapableHref` (pure unit coverage) and
`TestAdminPage_BareGetDoesNotEmbedNonFragmentCapableEntry` (the HTTP-level
reproduction — a cashier with `/open-orders` regrouped into Administration
gets an empty panel, not a nested document, and the tree does not mark
`/open-orders` `is-current`). Both verified via a real revert-then-restore
of `registerAdmin` back to the unconditional `groups[0].Entries[0].Href`:
the HTTP-level test failed with the actual nested `</html>` document
reproduced byte-for-byte, then passed again after restoring.

The reviewer separately noted a related, pre-existing, out-of-scope
observation: because the "administration" tile predicate is exactly `len(
visibleAdminEntries(...))>0`, the same kind of always-visible regrouped
entry also un-403s `/admin` itself for a role that should not otherwise
reach it — an unrelated always-visible page becoming the sole reason a
manager-only surface opens. This is a design consequence of ut-docs#2008/
#2116's own tile-visibility predicate, not something this diff introduces
or is scoped to fix, so it is left as a follow-up observation rather than
addressed here.

## Verified beyond automated tests

- `gofmt -l .` clean; `go build ./...`, `go vet ./...` clean; full
  `go test ./...` green; `golangci-lint run ./...` — 0 issues.
- CI-blocking guards: `guard-i18n.sh`, `guard-docs-shots.sh` (regenerated
  via `make docs-shots`, 124 screenshots across 31 topics × 4 locales),
  `guard-data-access.sh`, `guard-page-http-error.sh`, `guard-htmx-loaded.sh`
  all green.
- **Regression test verified via real revert-then-restore**: temporarily
  reverted `admin_tree.html`'s `{{ if .FragmentCapable }}...{{ end }}` back
  to the unconditional `hx-get`, confirmed
  `TestAdminPage_OtherClusterRowIsPlainNavigationNotFragmentSwap` fails
  with the real `hx-get="/users"` markup in its error message (not a
  compile error or a tautology), then restored the file and confirmed the
  test passes again — `git diff --stat` on the file was empty afterward.
- All of PR #1094's own admin-tree tests (`TestAdminPage_BareGetEmbeds-
  FirstVisibleEntryAndMarksItCurrent`,
  `TestAdminPage_BareGetRendersTheTreeExactlyOnce`, and every
  `Test{FiscalRegister,FiscalDevice,Locations,Registers,Translations,
  CountrySettings}Page_{HXRequest,NonHXRequest,VaryHXRequest}...` fragment
  test) still pass unmodified — this fix only narrows which rows get htmx
  wiring, it doesn't touch the six real destinations' own behavior.

## Safe-to-merge verdict

Yes. Both the click-path and arrival-path halves of the same bug class are
now fixed, each with a regression test verified via a real revert/restore.

## Explicitly deferred

The pre-existing `/admin` tile-visibility-predicate consequence the
reviewer separately flagged (an always-visible regrouped entry un-403ing
`/admin` for an otherwise-unprivileged role) is a design property of
ut-docs#2008/#2116, not introduced or fixed by this diff — noted here for
visibility, not filed as a new card, since it needs a product decision on
whether the tile predicate should instead require at least one
FRAGMENT-CAPABLE entry visible, not just any entry.
