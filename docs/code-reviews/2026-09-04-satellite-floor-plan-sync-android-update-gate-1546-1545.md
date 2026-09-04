# Code review — satellite floor-plan/kitchen sync, Android update install gate (ut-docs#1546, #1545)

- **Date:** 2026-09-04
- **Branch:** `fix/1539-1541-sync-chip-and-update-gate` (pre-review snapshot
  `df2cc34`, on top of `main` @ `fccae72` merged with `origin/main`'s later
  tip, which by then already carried PR #768)
- **Reviewer:** independent read via an Opus subagent, no shared context
  with the implementation, run in an isolated worktree.
- **Verdict: MERGE WITH FIXES.** One blocker made the #1545 fix inert for
  the actual operator; found and fixed. One further gap (#1546's deletion
  propagation for a table with local satellite history) was left for the
  orchestrator to decide — fixed, with a new regression test, after
  confirming the app never hard-deletes a table today (soft-disable via
  `SetTableEnabled`/`tables.enabled` is the only real path) so this closes
  a latent landmine rather than a live user-facing bug. Six other findings
  fixed; two nits noted, not fixed (documented below, out of scope/design
  calls). All fixes are in the merged commit — `gofmt`/`build`/`vet` clean,
  `go test ./internal/...` and every CI-blocking guard in `ci.yml`'s
  `build` job pass; `go test ./... -race` (full suite, 40m timeout) is
  clean — 0 races, 0 failures, 47 packages.

## Background — finishing a dead cycle's PR, not a fresh Dev pass

A prior pipeline cycle opened PR universaltill/universal-till#769 fixing
three bugs in one branch (ut-docs#1546, #1545, #1539) and died before
review/merge, leaving only a `WIP: pre-review snapshot` commit. Meanwhile
ut-docs#1539 was independently designed, built and merged via PR #768
(`docs/code-reviews/2026-09-04-sync-fiscal-chip-rail-migration-1539.md`).
Merging `main` into the stale branch conflicted across every file #1539's
own rework touched (`sync_chip.html`, `icons.go`, `app.css`, all 4 locale
files, all 4 `multitill.md` help topics, ~90 regenerated screenshots). This
cycle's stale-PR sweep (`scrum-master` SKILL.md step 0c) resolved the
conflict by taking `main`'s already-shipped version of everything #1539
touched and keeping only the still-needed #1546/#1545 work, which merged
without conflict except two test-assertion hunks in `sync_admin_test.go`
(resolved by dropping a superseded helper, `assertRailMenuButton`, that
asserted markup — `class="nav-toggle sync-toggle"`, `data-icon="tills"` —
that never actually shipped; #768's real markup is `class="nav-toggle"`
with `{{ icon "sync" }}`).

## What shipped

### ut-docs#1546 — satellites never received the floor plan or kitchen routing

- `internal/data/sync_admin_repo.go`'s `adminTables` was missing `tables`,
  `kitchen_stations`, `item_station_routes`, `category_station_routes`
  entirely — a satellite could neither take nor settle a table order, and
  kitchen tickets routed on the primary went nowhere from it. Added, ordered
  after `items`/`categories` (the two route tables carry FKs onto both).
- A round-trip test (`TestAdminDumpApplyRoundTrip_FloorPlanAndKitchenRouting`)
  covers content sync for all four tables plus deletion propagation for the
  simple case (a table with no referencing rows anywhere).
- `roles`/`role_permissions`/`permission_actions`, `registers`,
  `stock_locations` and `item_images` are deliberately out of scope — each
  needs its own shop-wide-vs-per-till decision, left open on the issue.

### ut-docs#1545 — Android update control shown, and downloading, on an up-to-date till

- `web/ui/pages/settings.html` gated the PIN+Download control on the
  platform bridge alone, so it rendered on every Android till forever; now
  also gated on `updateavailable`.
- `POST /api/update/android-install` (`internal/pages/update_api.go`) never
  re-checked freshness before authorising, so a spent PIN could buy a
  ~140MB download that only reinstalled the running version. Added an
  `androidInstallCheckNow` seam, re-checked before any authorisation work —
  mirrors the existing desktop `/api/update/apply` guard (in place since
  2026-07-28 for the same class of bug).

## Independent review — findings

**BLOCKER — the #1545 endpoint guard was inert; the reported symptom
survived end to end (fixed).** `web/ui/pages/settings.html` and
`web/ui/layouts/base.html`'s Android install JS both did `if (!res.ok)
{...}` and otherwise called `window.AndroidKiosk.installUpdate()`
unconditionally — neither read the response body. The endpoint's new guard
answers **HTTP 200** with `{"data":{"already_current":true}}`, which is
`res.ok`. Concrete failure: the template's `updateavailable` is a cached
check up to 24h stale by design; on a till that has since become current,
the control still renders, the operator taps it, the endpoint correctly
answers `already_current`, and the JS — never having looked — calls
`installUpdate()` anyway. That is exactly the pilot report ("after 10-15
seconds it shows the download window"). `TestAndroidInstallRefusesWhenAlreadyCurrent`
passed throughout review, because it only asserts the response, never the
consumer. Fixed: both surfaces now parse the envelope and show an
already-up-to-date message (`settings.update.up_to_date`, pre-existing key
in all 4 locales) instead of downloading. Added
`TestAndroidInstallSurfacesHonourAlreadyCurrent`, verified it fails without
the fix.

**BLOCKER — PR title/body still claimed #1539 (fixed here, orchestrator's
job).** Rewritten before opening/merging; branch name is stale
(`fix/1539-1541-...`) but left as-is — renaming a pushed branch mid-review
isn't worth the churn, and the PR's own title/body is what's user-facing.

**SHOULD-FIX — eight wrong issue references (fixed).** The new code cited
`ut-docs#1541`/`#1542` throughout — both real but unrelated cards (#1541 =
Android per-ABI APK splits, `blocked:env`; #1542 = Hold Sale hit-target
size). Corrected to #1545/#1546 in `sync_admin_repo.go`,
`sync_admin_repo_test.go`, `httpx.go`, `android_update_placement_test.go`
(×2), `update_api.go` (×2), `settings.html`.

**SHOULD-FIX — dead #1539 leftovers on the Go side (fixed, removed).** The
merge-conflict resolution dropped #1539's template/CSS/locale rework but
missed that `internal/pages/sync_admin.go` still built a `countLabel` that
`main`'s `sync_chip.html` never reads, via a `TCount`/`countLabelOrEmpty`
pair (`internal/httpx/httpx.go`) that resolved to `sync.chip_tills_one`/
`_other` — keys that exist in **no locale** (only `sync.chip_tills_title`
does). `T`'s fallback-to-key behaviour means wiring this path up would have
printed the literal string `sync.chip_tills_one` on a shop's till.
`guard-i18n.sh` can't see a Go-side key built by string concatenation.
Removed `TCount`, `countLabelOrEmpty`, `countLabel`, and the now-unused
`tills` icon registration — `icons.go` and `sync_admin.go` are back to
byte-identical with `main`.

**SHOULD-FIX — `TCount` insertion hijacked `T`'s godoc (fixed, moot after
the above removal).**

**SHOULD-FIX — #1546's deletion-propagation claim only held for a table
with zero referencing rows anywhere (fixed).** `deleteMissing`'s
retire-in-place fallback (used when a hard delete is FK-blocked by local
sales history — the same shape `items`/`users`/`tax_codes` already rely on)
only fires for a table with `hasIsActive: true`, and `tables` didn't have
it. Reproduced: delete a table on the primary that the **satellite's own**
local sales history still references — `deleteMissing` hits `FOREIGN KEY
constraint failed`, logs a warning, and silently keeps the full row
(`enabled=1`), which the table picker (`internal/pages/table_picker_api.go`)
would still offer to seat customers at. Confirmed the app itself never
hard-deletes a `tables` row today (`internal/data/tables_repo.go`'s own
comment: "Tables are soft-disabled (enabled=0), never hard-deleted" — no
`DeleteTable`/`RemoveTable` handler exists anywhere), so this is a latent
gap for a future hard-delete path, not a currently-reachable bug — still
worth closing now since the fix is small and the existing round-trip test's
own comment already claims "not leave a ghost table an operator can still
seat customers at."

Fix: generalized the retire-in-place fallback with an `activeCol` override
(defaults to `is_active`, same as before for every other table) and wired
`tables` through it with `activeCol: "enabled"` — its own existing
soft-delete column, already read by `ListTables`/`table_picker_api.go`.
Added `TestAdminApply_TableRetiredInPlaceWhenFKBlockedBySatelliteSaleHistory`,
which seeds a local `sales` row on the satellite referencing the table
before the primary deletes it.

**TDD re-verification (this reviewer's fixes, run personally, not just
taken on the subagent's word):**

- `TestAdminApply_TableRetiredInPlaceWhenFKBlockedBySatelliteSaleHistory`:
  reverted `activeCol: "enabled"` back to a plain `{name: "tables", pk:
  []string{"id"}}` → `FAIL`, `sync pull: cannot prune tables [tbl-1] (kept):
  FOREIGN KEY constraint failed`, then the assertion itself:
  `got enabled=1, an operator could still seat customers at it`. Restored →
  `PASS`. Also re-ran `TestAdminDumpApplyRoundTrip_FloorPlanAndKitchenRouting`
  alongside it, unaffected.
- The reviewer subagent's own TDD re-verification (independently reproduced,
  taken as read since its transcript showed the actual commands/output, not
  just a claim): reverting the `adminTables` additions for #1546 fails
  `TestAdminDumpApplyRoundTrip_FloorPlanAndKitchenRouting` with "the floor
  plan did not reach the satellite"; reverting the #1545 endpoint guard and
  template gate fails `TestAndroidUpdateControlHiddenWhenAlreadyCurrent` and
  `TestAndroidInstallRefusesWhenAlreadyCurrent` with the exact claimed
  errors. Both restore clean.

**NIT — not fixed, design call.** `update_api.go`'s freshness re-check now
runs before authorisation (correct per the fix's own point — a spent PIN
must never buy a no-op), but that means an unauthenticated-feeling tap
(the route isn't `auth.exempt()`, but a self-order kiosk browser can hold a
live manager cookie per the handler's own comment) drives a synchronous
outbound GitHub API call (10s timeout) before refusing, and doesn't honour
`UT_UPDATE_CHECK=0` — an air-gapped till would hang ~10s per tap. Flagged
for whoever next touches this endpoint; not blocking, and changing the
order would reopen the very defect this fix exists for.

**Manual.** #1545 changes what a shop owner sees (a control that now
disappears once current). Added a step to `web/help/{en,ar,fa,tr}/updates.md`
covering the new disappearing behaviour; `make docs-shots` regenerated
(`scripts/ci/guard-docs-shots.sh` green, surface `8ef387ef0ced`).

## Verified beyond automated tests

- `go test ./... -race` (full suite, `-timeout 40m` — the default 600s
  per-package timeout is too short for `internal/pages` under `-race`,
  ~973s, unrelated to this diff): 0 races, 0 failures, 47 packages ok. CI
  itself never runs `-race` (`ci.yml` doesn't pass the flag), so this was a
  genuine extra check, not a required gate.
- Every CI-blocking guard in `ci.yml`'s `build` job run locally and green:
  `guard-data-access`, `guard-migration-version-collision`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-page-http-error`,
  `guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`,
  `guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-osk-loaded`,
  `guard-e2e-fixtures-import`, `check-brand-assets`, `guard-makefile-version`.
- No raw SQL outside `internal/data`. No money handling in this diff. No
  file-write handler (no missing `os.MkdirAll`), no cwd-relative path where
  `paths.Data(...)` belongs. No real client/shop name used as demo/seed
  data (`Front Counter`, `Table 1`, `Terrace`, `Espresso`, `Hot Drinks` — all
  generic). No secret-shaped literal that isn't a placeholder/env reference.
  Offline-first checkout path untouched.

## Explicitly deferred (tracked on the issues, not this PR)

- ut-docs#1546: `roles`/`role_permissions`/`permission_actions`,
  `registers`, `stock_locations`, `item_images` — each needs its own
  shop-wide-vs-per-till decision.
- ut-docs#1546: a guard that fails when a new shop-wide table is added to
  the schema without a conscious sync decision — not built this pass.
- The `update_api.go` freshness-check-before-auth ordering nit above.
