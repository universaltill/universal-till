# Code review: Menu "Administration" group (ut-docs#1959)

**Date:** 2026-09-10
**Author:** Farshid Mirza (autonomous pipeline, `lane:cloud-24`)
**Reviewer:** independent Sonnet subagent, fresh context (complexity: easy)

## What shipped

Product-owner feedback (2026-09-10): group several Menu-screen settings
tiles — Registers, Locations, Fiscal register, Translations, and Country
settings — under an "Administration" heading, so day-to-day tiles aren't
mixed in with setup-once admin destinations.

The declarative UI-slot grouping mechanism (`internal/uislot`, ADR-0088)
already existed end to end (`Entry.Group`, `Resolve`/`groupTogether`,
`menu_page.go`'s `GroupHeading` render logic) — no new machinery was
needed, only:

- `internal/uislot/slot.go`: set `Group: "menu.group.administration"` on
  `/country-settings`, `/translations`, `/fiscal-register`,
  `/fiscal-device`, `/locations`, `/registers`. Moved `/report-issue`
  from Order 2700 to 2500 (ahead of the group) so the group's members
  stay declared **contiguously** — required because `Resolve()` never
  re-sorts/re-groups on the zero-amendment path (Decision I), so a
  non-contiguous declaration would render the same heading twice.
- `/users`, `/kitchen-stations`, `/bluetooth-devices` were considered
  (per the card) and deliberately left out of the group: Users is
  day-to-day staff management (the product owner's own call in the
  card); Kitchen Stations and Bluetooth Devices are reconfigured as
  often as the floor/hardware changes, not one-time setup.
- `/fiscal-device` (Turkey) was added to the group even though the
  product owner only named "Fiscal register" (Germany) explicitly — it
  is the structural twin of that entry (a statutory hardware/registration
  page connected once during setup), so the same reasoning applies.
- `web/locales/{en,ar,fa,tr}.json`: new key `menu.group.administration`
  ("Administration" / "الإدارة" / "مدیریت" / "Yönetim").
- `internal/pages/menu_layout_test.go`: updated
  `TestMenuPage_GoldenZeroPluginTileOrder`'s pinned tile order for the
  `/report-issue` move, and replaced its old "a zero-plugin till has no
  group headings" assertion (no longer true — core itself now declares
  one) with: a cashier (no settings permission) sees no `menu-group`
  markup; a manager sees **exactly one** Administration heading,
  positioned before its first member tile.
- `web/help/{en,ar,fa,tr}/menu.md`: new "Administration" section
  describing the group and why Users/Kitchen Stations/Bluetooth Devices
  are excluded. `web/help/img/manifest.json` regenerated
  (`make docs-shots`, scoped to the `menu` topic) — the actual PNGs came
  back byte-identical to what was committed, because the affected tiles
  sit below the fold at the 1024×600 reference viewport; verified this
  directly (manual curl of a freshly built binary showed the heading
  renders correctly server-side) rather than assuming.

## Independent review findings

Reviewed by a fresh-context Sonnet subagent (complexity: easy →
Sonnet-builds/Sonnet-reviews per the pipeline's model-routing rule),
isolated in its own worktree, instructed to actually build/test/run
guards rather than only read the diff.

**Verdict: safe to merge**, with two non-blocking findings, both
addressed before merge:

1. **Should-fix**: `TestCoreMenu_IsWellFormed` had no direct assertion of
   the group-contiguity invariant the new package-doc comment in
   `slot.go` describes in prose — it was only protected indirectly via
   the hand-pinned golden tile list in `menu_layout_test.go`.
   **Fixed**: added a `lastGroup`/`closedGroups` check to
   `TestCoreMenu_IsWellFormed` that fails if any `CoreMenu` entry reopens
   a group after a different group (or an ungrouped entry) interrupted
   it. Verified the new assertion actually catches the failure class it
   targets by temporarily un-contiguating the declaration in a scratch
   copy and confirming the test fails with the expected message, then
   restored the real file and re-ran the full build/test/lint/guard gate
   clean.
2. **Nit**: at review time the change was still a "WIP: pre-review
   snapshot" commit with no code-review record. Addressed by this
   document and the commit finalized below.

The review also independently confirmed (build+test, not just reading):

- Zero-amendment `Resolve()` never sorts/regroups (Decision I) — traced
  through `registerMenu`'s render loop line-by-line and confirmed an
  invisible (VisibleIf-gated) entry mid-group is `continue`d *before*
  `prevGroup` updates, so a DE/TR-only fiscal tile sandwiched in the
  group never causes a duplicate heading.
- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run` on
  the touched packages, and `go test ./internal/uislot/...
  ./internal/pages/...` all clean.
- `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` all pass; ar/fa/tr translations are plausible
  native terms, not placeholders.
- Manual prose order matches actual render order; the Users/Kitchen/
  Bluetooth exclusion is explained in the manual too.
- The `main`-detached-HEAD comparison it ran ruled out a slow
  `internal/pages` full-suite run as anything but pre-existing
  environment slowness unrelated to this diff (confirmed identical
  behaviour on `main` before this change).

## Verified beyond automated tests

- Manually built the binary and curled a live `/menu?lang=en` render
  under `UT_AUTH=off` to confirm the `<h2 class="menu-group">` heading
  actually appears in the server's HTML output (not just asserted by the
  test), before spending time on docs-shots.
- Confirmed via `git diff`/hash comparison that the regenerated
  screenshots are genuinely byte-identical to what's committed (not a
  script that silently skipped capturing) — the change is real but
  invisible at the reference viewport, not an untested no-op.
- Deliberately broke the group-contiguity invariant in a scratch copy of
  `slot.go` to confirm the new regression test fails the way it's meant
  to, then restored and re-ran the full gate.

## Deferred / out of scope

- Reusing one shared two-pane primitive across `/items` (ut-docs#1950),
  `/settings` (ut-docs#1960) and the existing `/help` shell — noted as
  future work on those cards, not this one (this card only touches Menu
  grouping, not a two-pane layout).
- `Kitchen Stations`/`Fiscal device` groupings for verticals other than
  the ones already gated (`VisibleIf`) are unaffected — no new gate
  logic was introduced.

## Safe-to-merge verdict

Yes. Build, tests, lint and all four relevant CI guards pass; the
independent review found no blocking issues, and the one should-fix
finding was fixed and re-verified before this record was written.
