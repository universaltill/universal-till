# Code review — single gated Administration tile + tree page (ut-docs#2008)

- **Date:** 2026-09-10
- **Branch:** `feat/2008-admin-tree-menu-entry`
- **Reviewer:** two independent rounds, both fresh-context Opus subagents in
  isolated worktrees (`complexity:medium` review tier — Sonnet built it).
  A second round ran because the first found a compliance-relevant blocker
  (statutory-page reachability); per the reviewer skill's own rule it stayed
  scoped to the fixes, not a re-review of the whole diff.
- **Verdict: SAFE TO MERGE.** No blocking findings survive; two rounds of
  blockers were found and fixed in this same branch (see below). One
  non-blocking finding (a transient snapshot-read divergence) accepted as
  real but out of scope; two nitpicks fixed, one accepted as pre-existing.

## What shipped

Replaces ut-docs#1959's flat-grid "Administration" group heading (six
setup/onboarding destinations under one `<h2>` on `/menu`: country
settings, translations, the two statutory fiscal-device pages, stock
locations, registers) with a single gated "Administration" tile that opens
a new `GET /admin` tree page listing the same six, grouped into three
domain clusters (Fiscal / Locations / Localization). The tile disappears
entirely for a viewer who can see none of the six; `/admin` 403s
independently of the tile's own visibility — reaching it directly by URL
with no permission refuses, the same "visibility is not the security
boundary" rule every other admin page in this product already enforces on
itself. Reuses `internal/uislot`'s existing `Entry`/`Resolve` mechanism
(ADR-0088) rather than inventing new grouping machinery — no new ADR
needed for the base feature, since the package's own doc comment already
anticipated further slots attaching this way.

## Independent review — round 1

Dispatched to a fresh-context Opus subagent, isolated worktree, told to
actually run things and find real problems.

**Verified by running:** `go build`, `go vet`, `go test ./internal/pages/...
./internal/uislot/...` (all green, no `-race` — this package's `-race`
runtime is a documented ~25min pre-existing characteristic with no CI
margin, see `Makefile`'s `test-race-pages` comment and
`docs/code-reviews/2026-09-10-pages-ci-timeout-margin-1992.md`; CI never
runs `-race` on it), `guard-i18n.sh`, `guard-help-topics.sh`,
`guard-help-drift.sh`, `guard-page-http-error.sh`.

**Blocking, found and fixed:**
1. `guard-docs-shots.sh` failed — `web/help/*/menu.md` changed but the
   manual's screenshots weren't regenerated. Fixed: `make docs-shots`.
2. `/admin` wasn't in `uislot.ProtectedMenuKeys`, but it had just become the
   ONLY menu path to `/fiscal-register` (§146a Abs. 4 AO) and
   `/fiscal-device` (Turkish YN ÖKC) — hiding or relabelling the single tile
   would make both statutory pages unreachable from any UI surface, the
   exact failure ADR-0088 Decision E exists to refuse. Fixed: added
   `/admin` to `ProtectedMenuKeys`.
3. `visibleAdminEntries` read raw `uislot.CoreMenu` instead of the same
   plugin-amended `uislot.Resolve(...)` output `registerMenu` itself
   builds — a `layout` plugin's hide/regroup-in/regroup-out amendments on
   the six entries produced wrong results on `/admin` (no-op hides,
   entries vanishing from both surfaces, or duplicating across both).
   Fixed: rewrote it to resolve the identical list `registerMenu` computes.

**Non-blocking, found and fixed:**
4. The doc comment on `adminGroupsFor` claimed a test
   (`TestAdminPage_EveryVisibleAdminEntryHasAGroup`) that didn't exist.
   Fixed: added the real test (renamed
   `TestAdminGroupsFor_EveryCoreAdministrationEntryClaimedByExactlyOneNamedCluster`
   during round 2's nitpick pass) and, since fixing (3) surfaced a real gap
   the missing test would have caught (a plugin regrouping some OTHER core
   entry INTO the administration group has no named cluster to land in),
   added an "Other" catch-all cluster (+ `admin.group.other` in all 4
   locales) so a visible entry never silently disappears from the tree.
5. Regrouping `/admin` into its OWN group (a validated amendment — protected
   keys stay re-groupable) would recurse `visibleAdminEntries` into itself
   via the `"administration"` `VisibleIf` predicate — unbounded recursion,
   a stack-overflow crash on every `/menu`/`/admin` request. Fixed: an
   explicit `Key=="/admin"` skip inside `visibleAdminEntries`'s filter loop.
6. Three heading-presence test assertions were tautologies: a bare
   `strings.Contains(body, httpx.T("en", key))` passes even with the
   `<h2>` deleted, because a sibling row's own label text (`locations.title`
   is literally `"Locations"`, identical to `admin.group.locations`)
   already contains the same substring. Fixed: `adminGroupHeading`/
   `adminPageTitle` helpers require the exact tag wrapper.
7. `.admin-tree .items-row`'s touch target was 44px against the product's
   own documented 46px floor (`.btn-touch`). Fixed: raised to 46px.

## Independent review — round 2 (scoped to round 1's fixes)

**Verified by running**, including two destructive revert-then-restore
checks (byte-clean afterward): confirmed the three new regression tests
for finding 3 above actually fail against the pre-fix code; confirmed the
tautology fix for finding 6 actually fails with the `<h2>` deleted,
specifically for the `admin.group.locations` case that was the live
tautology. Confirmed the "Other" cluster always renders last regardless of
input order. Confirmed hiding `/admin` is genuinely refused. Confirmed
`TestVisibleAdminEntries_ExcludesAdminItselfEvenIfRegroupedIntoItsOwnGroup`
passes with no hang/crash. All guards, build, vet, and the full
`internal/pages`/`internal/uislot` suite green throughout.

**Blocking, found and fixed:**
1. Finding 2's fix (adding `/admin` to `ProtectedMenuKeys`) only blocks
   *hide*. Protected keys stay re-groupable by design, and
   `{"key":"/admin","group":"menu.group.administration"}` reproduces the
   exact same unreachability through a completely different mechanism:
   `registerMenu`'s own flat-grid skip is keyed on `Group ==
   "menu.group.administration"`, with no exemption for `/admin` itself —
   so regrouping `/admin` into that value makes it satisfy its own skip and
   vanish from `/menu`, while finding 5's `Key=="/admin"` guard
   simultaneously keeps it out of the tree. Verified live with a probe
   amendment before the fix: tile absent from `/menu`, absent from
   `/admin`, both statutory pages unreachable. Fixed: `registerMenu`'s skip
   condition gained the same `Key != "/admin"` exemption, and
   `visibleAdminEntries`'s recursion guard was hardened to also key on
   `VisibleIf=="administration"` (the actual recursive trigger, not just
   the one key it happens to apply to today). `TestMenuPage_AdminTileRendersRegardlessOfGroupAmendment`
   pins this directly — confirmed to fail against the pre-fix code.
2. Extending `ProtectedMenuKeys` (an ADR-0088 Decision E enumeration)
   without amending the ADR contradicted an accepted, binding document
   (`ut-docs/CLAUDE.md`'s document-first rule, ADR-0007) — the doc
   literally said "seven" while the code now said eight. Fixed: amended
   ADR-0088 Decision E in `ut-docs` (dated, in-place, matching the ADR's
   own established practice of revising a decision when review changes the
   reasoning — Decision E already carries one such revision from its
   original drafting) to add `/admin`, explain why regroup alone
   reproduces the hide-class failure for this one key, and record the
   render-time carve-out as the fix (not a narrowing of regroup itself,
   which remains permitted and correct for every other value).

**Non-blocking, accepted, not fixed (real but out of scope or genuinely
trivial):**
- Two independent `MenuSnapshot()`/`MenuAmendmentsSnapshot()` reads per
  `/menu` request (one in `registerMenu`, one inside `visibleAdminEntries`)
  can observe different generations if a plugin reload lands mid-request —
  transient, self-correcting (the next request is consistent), not data
  corruption. A shared per-request snapshot would close it but is a larger
  refactor than this card's scope.
- `visibleAdminEntries` re-evaluates `"settings"` and re-runs
  `menuSlotEntries`+`Resolve` a second time per `/menu` render (the
  existing `menuVisibility` memo doesn't cross the two separate instances).
  One extra permission lookup per render; trivial.

**Nitpicks, fixed:**
- A comment referenced the test's old, un-renamed name.
- `admin_tree.html`'s own comment still said "44px" after finding 7 above
  raised it to 46px.

**Nitpick, accepted, not fixed (pre-existing, unrelated to this card):**
- A plugin `page` entry declaring `href: "/admin"` would render two `/admin`
  tiles on `/menu` (`menuSlotEntries` doesn't dedupe a plugin page entry
  against a same-keyed core entry). This is a general, pre-existing
  characteristic of the menu-building path — any core key could collide
  the same way — not something this card introduced or scoped to fix.

## Verified beyond automated tests

- Drove the real app (throwaway till, `UT_AUTH=off`, demo catalogue) and
  looked at actual screenshots: `/menu` (single Administration tile, gear
  icon distinct from Settings' own) and `/admin` (grouped tree, back-to-menu
  button) at 1024×600 and 360×800, in `en`, `fa` (RTL — nav rail and text
  correctly mirrored, real Persian translations render, no clipping),
  `ar` (RTL, same), and `tr`. No overlap, clipping, or broken wrapping in
  any of them. `de-DE` was also tried and correctly falls back to English
  (there is no shipped `de.json` UI locale — `web/locales/` only ships
  `en`/`ar`/`fa`/`tr` — so this is expected graceful-fallback behavior, not
  a gap in this card).
- Manually confirmed all six destination handlers already had their own
  independent server-side `canPerform`/`requireManager`-equivalent gate on
  every route (GET and POST alike) before this card — none needed a fix —
  by reading every `mux.HandleFunc` in `country_settings_page.go`,
  `translations_page.go`, `fiscal_register_page.go`, `fiscal_device_page.go`,
  `locations_page.go`, `registers_page.go` and confirming a gate call
  immediately follows each one.
- Spot-checked the `ar`/`fa`/`tr` translations for all 4 new/changed i18n
  keys (`admin.group.fiscal`/`locations`/`localization`/`other`) against
  existing terminology already used elsewhere in the same locale files for
  the same underlying concepts — genuinely idiomatic, not machine-literal.

## Explicitly deferred

- No plugin-amendment path for the `/admin` tree's own membership in this
  iteration (core-only: the six destinations are fixed by `Group`
  membership, not separately configurable) — noted as a natural follow-up
  if a plugin ever needs to contribute a new admin destination into the
  tree itself, not just regroup among the existing six.
- The transient snapshot-divergence and double-evaluation findings above
  (round 2's non-blocking items).
- No hardware verification — no physical pilot tablet reachable from this
  session; visual checks above were an emulated 1024×600/360px browser
  check via the pre-installed Chromium, stated explicitly per the UX role's
  own rule rather than implied as hardware-verified.
