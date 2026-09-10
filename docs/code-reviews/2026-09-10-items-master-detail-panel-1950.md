# Code review: /items master-detail panel (ut-docs#1950)

**Date:** 2026-09-10
**Card:** ut-docs#1950 — "/items should be master-detail with a right-hand
panel, and its section rows are far too large" (product-owner feedback
comparing `/items` against the SumUp Business app).
**Branch:** `feat/1950-items-master-detail-panel`
**Reviewer:** independent (Opus, fresh context, isolated worktree, did not
write the implementation)

## What shipped

`/items` is now a two-pane master-detail screen at tablet width and up,
mirroring `internal/pages/help_page.go`/`web/ui/pages/help.html`'s existing
HX-Request-vs-full-page pattern rather than inventing a new one:

- A compact left rail (`internal/pages/itemsnav`, `web/ui/partials/items_rail.html`)
  replaces the old large card-tile stack. Each row shows the section name
  and its subtitle as a real second line (not a hover tooltip — see
  Findings), truncating independently with ellipsis rather than wrapping.
- The five section destinations (`/catalog`, `/categories`, `/inventory`,
  `/modifiers`, `/catalog/option-sets`) each gained a fragment-vs-full-page
  branch (`httpx.IsFragmentSwap`/new `httpx.RenderContentFragment`,
  factored out of `help_page.go`'s original inline logic): a plain browser
  GET still renders the complete standalone page unchanged; an
  `HX-Request: true` GET (excluding `HX-History-Restore-Request`, same
  distinction `help_page.go` already makes) renders just the page's
  `content` template block for the right panel, plus an out-of-band rail
  swap (`itemsnav.WriteRailOOB`) so the active-row highlight follows the
  click.
- `/items`'s own default view (AC: "first section selected by default,
  panel never empty on arrival") is filled via an in-process sub-request to
  `/catalog` with `HX-Request: true` set — the same idiom
  `import_stage.go`'s `commitStagedImportForSetup` already uses, reusing
  `/catalog`'s real handler instead of duplicating its data/render logic.
- Below the existing `.manual` 52rem breakpoint (reused deliberately, not
  re-derived), the two panes stack and a rail tap falls back to a real
  navigation instead of the in-panel swap — implemented as a capture-phase
  click listener bound on `document` (see Findings for why not the rail
  itself) that lets the plain `href` through under `matchMedia`.

## Independent review — one blocker found and fixed, one real CI-blocking gap closed, several judgment calls resolved

**Blocker (fixed): `/items` rendered the whole section list twice on
every load.** The in-process sub-request to `/catalog` set `HX-Request:
true`, so `/catalog` took its new fragment branch — which also emitted its
out-of-band rail swap, inlining a second `id="items-rail"` and 10 rows (5
real + 5 duplicated) into the panel on a bare `GET /items`. Fixed by adding
`itemsnav.EmbedHeader`, an internal-only header the embedding sub-request
sets and `WriteRailOOB` checks to skip its OOB copy when the response is
being embedded rather than swapped directly. Two regression tests added:
one pinning exactly one rail + 5 rows on a bare load, one pinning that a
*real* htmx swap still gets its OOB rail.

**CI-blocking gap (closed): `guard-docs-shots.sh` failed.** The diff
changed `web/ui/**`/`web/public/**`/`internal/pages/**.go` and four
`catalog.md` topic files without regenerating the manual's screenshots —
correctly flagged by the guard as stale, not a false positive. Closed by
running `make docs-shots` for real (this session has a pre-installed,
network-independent Chromium unlike the environment the initial
implementation and first review pass ran in) — 112 screenshots
regenerated, guard now passes (`28 routed topics × 4 locales screenshotted
and fresh`). Only `web/help/img/{en,ar}/sell.png` show an actual pixel
diff (the dashboard's Items tile rendering); every other topic's
screenshot was byte-identical, confirming the CSS/markup changes are
correctly scoped to `/items` and its five destinations.

**Should-fix, resolved (reverted): the categories-mutation `HX-Redirect`
change.** An earlier draft replaced ten `http.Redirect` 303 call sites in
`categories_page.go` with a helper that answers `HX-Redirect` instead, on
the theory that a bare 303 wouldn't reliably re-trigger an in-panel swap
for an embedded mutation form. Independent review found this backwards —
`categories.html`'s forms are plain `<form method="post">` with no
`hx-post`/`hx-boost` anywhere in the layout, so the branch was unreachable
dead code today, and if the forms were ever converted to `hx-post`, htmx
already follows a plain 303 on its own AJAX requests and would swap
normally — `HX-Redirect` would force an *unwanted* full-page reload
instead. The two tests supporting the change set `HX-Request: true` by
hand, so they proved the helper does what it was written to do, not the
underlying browser-behavior claim. Reverted to plain `http.Redirect`
(behavior for `/categories` is unchanged from before this card); the two
now-inapplicable tests were removed. Revisit if/when these forms actually
gain `hx-post`.

**Should-fix, resolved (redesigned): subtitles in a `title=""` tooltip.**
An earlier draft moved each row's subtitle into a hover tooltip to keep
the row single-line. `reference/ux-guidelines.md` rules this out
categorically — this is a touchscreen till with no mouse, and "nothing
depends on hover-only affordances (tooltips...)" is a direct rule, not a
judgment call; a tooltip-only subtitle is simply unreachable on the pilot
tablet. Redesigned as a real second line (`.items-name`/`.items-subtitle`
inside `.items-text`, each ellipsing independently via `min-inline-size: 0`
on the flex column — the same class of overflow bug ut-docs#1900 already
found and fixed on the catalog variants grid). Rows are still considerably
smaller than the old ~6rem+ tiles at roughly 3.4rem for two compact lines,
comfortably clearing the 44px touch-target guidance.

**Should-fix, resolved (added): no error/empty state on a failed embed.**
`embedItemsSection` returned an empty string on any non-200 from its
sub-request (a permission gate, a DB error), which both silently violates
ux-guidelines.md's "no silent failures — every user-facing action needs a
real error/empty/loading state" and defeats the card's own "panel never
empty on arrival" AC exactly when something is actually wrong. Fixed: logs
the real status/body via `logging.L().Errorf` (same pattern
`commitStagedImportForSetup` already uses) and renders a translated
(`common.error.server`) fallback card instead of nothing.

**Nitpick, resolved: `ResolveLocale` called after the response body had
already been written** in several of the five handlers, silently dropping
a `?lang=` cookie set. Switched to `httpx.RequestLocale(r)`, the codebase's
own side-effect-free variant for exactly this ordering hazard.

## Verified beyond automated tests

- `gofmt -l .` / `go vet ./...` / `go build ./...` — clean.
- `go test ./...` — full suite, all green, no regressions from the
  triage fixes above.
- `golangci-lint run ./...` — 0 issues.
- `scripts/ci/guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`
  — all pass (see above for the docs-shots regeneration).
- TDD claim re-verified personally (not just taken on report): reverted
  `redirectCategories` back to a bare `http.Redirect`, confirmed the two
  helper-behavior tests failed with the expected messages, restored, and
  confirmed green again — then those two tests were removed anyway per the
  revert decision above, since the behavior they tested no longer exists.
- The in-process embed sub-request idiom was checked against
  `net/http.ServeMux`'s real locking behavior (its `RLock` is released
  before the handler runs, so the nested `ServeHTTP` call is not
  re-entrant-locking) and against `auth.Middleware` (session-establishment
  only, no per-route RBAC — nothing is escalated by bypassing it via the
  raw mux).
- No client/shop name used as test/demo data; no literal secrets.

## Explicitly deferred, not blocking this merge

- **`ut-plugin-language-{de,es}` follow-up for `items.rail.label`.** This
  is a brand-new `web/locales/en.json` key, so per ut-docs#1934's ruling
  (a brand-new key can't be pack-first — the pack's own orphan guard would
  reject a translation with no matching core key yet), core lands first
  and `main` goes red on `lang-pack-drift` until the pack PRs land. Owned
  by whoever merges this PR, in the same session, per
  `scrum-master/SKILL.md`'s "work that has no card is not covered" rule.
- **German label-length check.** `ut-plugin-language-de` wasn't attached
  to the sessions that built or reviewed this; `overflow: hidden;
  text-overflow: ellipsis` on `.items-name`/`.items-subtitle` is the
  safety net regardless of actual German string length.
- **Real-device/tablet check of the phone-width fallback and the
  capture-phase click-interception script.** Verified correct against
  htmx 1.9.12's vendored source (per-element `addEventListener` binding,
  `hx-swap-oob="true"` is an outerHTML swap) and rebound on `document`
  (survives every swap) rather than the rail itself, but this is DOM/event
  reasoning, not a real-browser observation — flagged for `ux`/`tester` to
  confirm on the actual 10.1" pilot tablet.
- `ut-docs#1911` (ADR-0088 slot-registry conversion of the section list)
  remains separate, unclaimed work — this PR deliberately built on the
  existing hardcoded `itemsnav.Sections` list per the card's own
  instruction, relocated (not redesigned) into its own leaf package only
  because `internal/pages/catalog` needed to reach it without an import
  cycle. `#1911` will need to account for the two-pane wrapper this PR
  adds; noted in `items_page.go`'s doc comment.

## Verdict

**Safe to merge**, with the two explicitly-deferred items above (pack
follow-ups, a real-tablet UX pass) tracked as separate follow-up work, not
blockers to this PR.
