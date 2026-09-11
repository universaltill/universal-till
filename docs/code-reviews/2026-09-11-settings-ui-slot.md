# 2026-09-11 — Settings sidebar as an ADR-0088 UI slot (ut-docs#1913)

## What shipped

Extends the existing ADR-0088 "declarative UI slot registry" (already
shipping for the Menu, Items and Rail slots) with a fourth slot: the
`/settings` page's own sidebar section list.

- `internal/uislot`: new `SettingsSlot`, `CoreSettings` (the page's 25
  `.card` sections declared as data, in their existing order/labels),
  `ProtectedSettingsKeys` (`settings-data`, `settings-retention`,
  `settings-all` — compliance/destructive-data-adjacent, or the raw
  unbounded key/value browser), `settingsSpec`/`ParseSettingsAmendments`,
  wired into the slot dispatcher.
- `internal/pages/common`: `SettingsAmendments` field/snapshot/builder,
  mirroring the three existing twins (Menu/Items/Rail).
- `internal/pages/settingsnav` (new package): resolves core defaults +
  active amendments into display rows for the sidebar, with a small
  per-key emoji-prefix map so a zero-plugin render is byte-identical to
  the pre-existing hand-written headings.
- `internal/pages/settings_page.go`: wires the resolved rows into the
  render data, filtered per-request through `filterSettingsNavForRender`
  so a `.card` this specific request's own gates (isManager / payMethods
  / the Data card's 4-way OR) won't actually render is never named in the
  sidebar index either.
- `web/ui/pages/settings.html`: a new hidden `#settings-nav-index` data
  island carries the resolved rows; the sidebar-building JS reads it
  (falling back to the old DOM-scan if the index is empty) and now
  renders group headings; `restoreNav` (search) replays the original
  nodes so group headings survive a search-then-clear cycle.
- `web/public/app.css`: `.settings-tree-group` heading style.
- `plugins/layout-salon`: a new Settings-slot amendment (reorder Theme to
  the front, group Printer with Tills under "Hardware") demonstrates the
  mechanism end to end; version 0.3.0 → 0.4.0; the new
  `layout.salon.settings_group` key ships in all 6 plugin locale files
  with real (non-English-duplicate) translations.
- `scripts/ci/guard-plugin-menu-read.sh`: extended to also guard the new
  `SettingsAmendments` field and `BuildSettingsAmendments` call sites.
- `web/help/en/menu.md` / `en/display.md`: the manual's "what a layout
  plugin can do" section and the shop-type walkthrough both now mention
  the Settings-slot reorder/group; `web/help/img/**` regenerated
  (`make docs-shots`).

**Deliberate scope decision, documented in the code itself**
(`uislot.CoreSettings`'s doc comment): unlike Items/Rail, this slot
resolves the sidebar's order/label/grouping only — it does not hide,
reorder, or relabel the actual on-page `.card` content/headings, and hide
is refused for the whole slot (same precedent as Items/Rail: no restore/
findability surface exists yet). Physically reordering ~2,100 lines of
interleaved elevation-gated settings forms, and building that restore
surface, is real further work, filed as a follow-up rather than folded
into this slice — matching how ADR-0088 itself shipped one slot at a
time.

## Independent review (Opus, fresh context, isolated worktree)

Ran build/vet/lint/the full relevant test suites and every CI-blocking
guard; read the diff for correctness, security, i18n and manual-drift
issues; did a real revert-then-restore TDD check on the leak-prevention
fix described below.

**One BLOCKER found and fixed**: `internal/pages/init.go`'s `common.Deps`
struct literal set `MenuAmendments`/`ItemsAmendments`/`RailAmendments` but
not `SettingsAmendments` — `ReloadPlugins` was the field's only other
assignment site, and boot only calls that conditionally (ut-docs#2006's
"skip a genuine no-op reload" optimization). On a steady-state till
reboot (the common case — nothing changed since the last boot),
`SettingsAmendments` stayed empty and a `layout` plugin's Settings-slot
amendment was silently inert until some unrelated event happened to
trigger a reload. Fixed with a one-line addition to the struct literal.
TDD-verified for real: `TestInit_SteadyStateRebootPopulatesSettingsAmendmentsWithoutReload`
(new — boots twice against the same DB so the second boot is a genuine
Sync no-op) fails without the fix and passes with it; confirmed by
reverting the fix locally, re-running, and restoring.

**Three should-fix items, all addressed**:

1. The manual's layout-plugin explanation (`menu.md`) and the shop-type
   walkthrough (`display.md`) didn't mention the new Settings-slot
   capability — a real gap per the standing "manual ships with the
   feature" rule, with direct precedent: the immediately preceding slot
   (Rail, ut-docs#1912) shipped with the identical gap and needed its own
   follow-up fix (`fix/2053-rail-relabel-help-text`). Both topics updated
   in this same branch; `guard-help-topics.sh`/`guard-help-drift.sh` stay
   green (no heading/step/bullet count changed, only sentences extended).
2. Nothing tied `uislot.CoreSettings`' declared keys/order to the
   template's actual `.card` ids — a future card added to
   `settings.html` with no matching registry entry would silently vanish
   from both the sidebar *and* search (the review verified today's state
   is correct by hand). New test
   `TestSettingsPage_CoreSettingsAndFilterMatchTheRealTemplate` reads the
   real template and asserts the key sets and order match exactly.
3. `filterSettingsNavForRender`'s hardcoded 5-key gated-set had nothing
   tying it to which cards the template actually wraps in a `{{ if }}` —
   the failure mode here is a leak (a future gated card's title showing
   in the sidebar for a viewer who can't see the card itself), the exact
   class the two existing tests
   (`TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending`,
   `TestSettingsPage_NavIndexNeverLeaksManagerOnlyRowsToCashier`) exist to
   prevent. The same new test above cross-checks the filter's map against
   a real per-card `{{ if }}` scan of the template and fails if they
   diverge (verified: temporarily dropping `settings-all` from the map
   fails the test).

Two nitpicks noted, not actioned (genuinely cosmetic / already
self-consistent): a stale "built from the DOM" comment in
`settings_two_pane_test.go`, and the default landing section on load
still following DOM order rather than the resolved sidebar order when a
plugin reorders — self-consistent under this slice's declared
sidebar-only scope, left for the content-reorder follow-up.

## Verified beyond automated tests

- Full `go build ./...`, `go vet ./...`, `gofmt -l .` (clean), and
  `golangci-lint run ./...` (0 issues).
- Full `go test ./...` — all packages green (including the pre-existing,
  now-extended `internal/uislot`, `internal/pages`,
  `internal/pages/settingsnav`, `internal/plugins`,
  `internal/plugins/builtinlayouts` suites).
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally and green, including `guard-plugin-menu-read.sh` (extended for
  this change), `guard-i18n.sh` (no new core locale keys needed — the
  demo plugin's new key lives in the plugin's own locale files, per
  ADR-0088 Decision G), `guard-help-topics.sh`/`guard-help-drift.sh`
  (manual topics updated, no structural drift introduced), and
  `guard-docs-shots.sh` (screenshots regenerated via `make docs-shots`
  using the pre-installed fallback Chromium — `/settings`'s own
  screenshot is byte-identical across the whole run, confirming "zero
  visible change on a zero-plugin till"; a few unrelated pages'
  screenshots (`catalog`, `sell`) shifted by a few bytes between runs,
  attributable to the fallback browser's point-version mismatch against
  the Playwright pin, not to this diff — those pages' own templates are
  untouched here).
- Independent review additionally verified the security posture of the
  new `settingsSpec` by hand: hide/icon refused slot-wide, protected keys
  refuse relabel while permitting reorder/group, no field-name or
  case-sensitivity bypass found, and validation re-runs at plugin *load*
  time (not just install), so a hand-edited DB row can't smuggle a
  refused amendment past the guard.

## Safe-to-merge verdict

Yes, after the blocker fix and the three should-fix items above, all
independently re-verified.

## Explicitly deferred (follow-up work, not silently dropped)

- Physically reordering/relabeling/hiding the on-page `.card` content
  itself (not just the sidebar), plus the "Settings → Hidden sections"
  restore/findability surface `allowHide` needs before it can be widened
  — mirrors how Items/Rail deferred the same thing. Tracked as a new
  Backlog card by this cycle's Scrum Master pass.
