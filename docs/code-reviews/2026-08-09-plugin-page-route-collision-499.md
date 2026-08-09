# Code review: plugin page entries collide on shared ROUTE, not just KEY

**Card:** universaltill/ut-docs#499
**Date:** 2026-08-09
**Complexity:** medium — Dev inline (Sonnet), Review via independent Opus
subagent (isolated worktree), per this pipeline's model-routing rule. One
review round: the round found two should-fix items, both folded in this
same cycle and self-verified (build/vet/tests/guards + TDD revert-checks
on each) rather than spun into a second full Opus pass — neither finding
was money/tax/data-loss/security class, the bar this pipeline's
process-depth rule sets for earning a second full independent round.

## What shipped

`internal/pages/plugin_page.go`'s `findPageEntry` resolves `GET /plugin/…`
by matching `route` against `ListPageEntries`'s rows (`ORDER BY
sort_order, plugin_id, key`) and returning the **first** hit — unguarded,
so two plugins declaring *different* keys but the *same* route both
installed cleanly, and whichever sorted first silently served every
request to that route with no error and no signal to either plugin
author or the shop owner. Found by the independent reviewer of
ut-docs#472 (the sibling `key`-collision fix, shipped same day) while
probing a third dispatch path that diff didn't cover — filed as this
card rather than folded into #472's own diff.

Extends #472's install-time guard pattern to this second, independent
namespace — route instead of key — at the same two call sites
(`PersistManifest`, `RollbackManager.Rollback`):

- **`internal/data/plugin_repo.go`**: new `PageRouteConflict` +
  `FindPageRouteConflicts(ctx, tx, pluginID, routes)` — per candidate
  route, checks whether another plugin already owns a `type='page'` entry
  with that route in `plugin_entries`. Excludes the plugin's own rows
  (self-upgrade stays clean), mirroring `FindPageKeyConflicts`.
- **`internal/plugins/manifest.go`**: new `validatePageEntryRoutes` —
  skips entries with no route (not dispatchable, can't collide), checks
  cross-plugin conflicts for every non-empty route, **and** — added
  during review, see below — rejects two entries in the *same* manifest
  sharing a route. Wired into `PersistManifest` right after the existing
  key check.
- **`internal/plugins/rollback.go`**: `RollbackManager.Rollback` calls
  the same function, so a legacy on-disk manifest predating this check
  can't restore a colliding route either.
- **`internal/plugins/install_status.go`**: `ClassifyInstallError` gets a
  new case mapping a page-route-collision error to
  `plugins.install.error.page_route_conflict`, `Retryable: false` — same
  shape as the existing `page_conflict`/`payment_conflict` cases, scoped
  tightly enough (`"page entry route"` vs `"page entry key"`) not to
  cross-match the sibling case.
- New locale key `plugins.install.error.page_route_conflict` in all 4
  `web/locales/*.json` files.
- New tests: `page_route_validation_test.go` (cross-plugin route
  collision rejected + first plugin's row/dispatch untouched, self-upgrade
  allowed, empty route never checked, within-manifest duplicate rejected,
  rollback rejection), an `install_status_test.go` addition
  (classification + locale resolution), and an end-to-end
  `internal/pages` test (`TestPluginPage_RouteCollisionRejectedAtInstall_
  FirstPluginStillServes`) confirming `GET /plugin/<route>` keeps serving
  the first (accepted) plugin's content after the second's rejected
  install — this ticket's literal acceptance criterion, exercised through
  the real HTTP handler, not just a DB-row assertion.

### No docs exemption, unlike the sibling key check

`validatePageEntryKeys` exempts `DocsEntryKey` ("docs") from its
cross-plugin check, because two plugins sharing `key:"docs"` never
collide via `MenuPlugins` in any live path. **This diff exempts nothing.**
ADR-0037 has every docs entry declare its *own* route
(`"route": "/plugin/<its-usual-route>"`) — so two plugins sharing a route,
docs-keyed or not, is always a genuine authoring conflict, never the
convention's expected shape. Confirmed by reading ADR-0037 directly
(both Dev and the reviewer independently), not inferred from the diff's
own comment.

This has a real consequence for existing test coverage:
`page_key_validation_test.go`'s `pageManifest` helper derived
`route = "/"+key`, so `TestPersistManifest_DocsKeyExemptAcrossPlugins`
(both plugins using `key:"docs"`) would otherwise install both under the
identical route `/docs` — a genuine route collision this new check is
required to reject, which would have broken a previously-green test for
an unrelated reason. Fixed by giving each docs install its own route
(`/plugin/first-docs`, `/plugin/second-docs`) via a new
`pageManifestRoute` helper, matching ADR-0037's real convention instead
of accidentally exercising the collision case.

## Independent review (Opus, isolated worktree)

Ran the full gate itself (build/vet/gofmt/tests -race/all 4 CLAUDE.md
guards, plus `guard-help-topics.sh`), independently re-verified the TDD
claim by reverting only the four production files and confirming the new
tests fail with the exact claimed error strings, then restored and
confirmed green. Went beyond the diff's own scoping claim and probed
every other route/key resolution path itself rather than trusting the
commit message: `plugins_page.go`'s `docsRouteByPlugin` (keyed by plugin
ID, not route — no separate guard needed), `external_api.go` (resolves
via `MenuPlugins`/key, already covered by #472, not a route resolver),
`common/state.go`'s nav-item builder (fed by the same now-guarded
`Route` field), and confirmed `ReplacePluginEntries` has exactly two
production callers, both now guarded — no bypass path. Also checked the
`route` column's schema (`TEXT`, no `COLLATE`) against Go's `==`
comparison for a collation mismatch — none.

**Verdict: NOT safe to merge as-is** — zero blockers, two should-fix,
three nitpicks/informational.

### Should-fix — both folded in this cycle

1. **Within-manifest duplicate routes were unguarded, with no DB
   backstop.** `FindPageRouteConflicts` only compares against *other*
   plugins — one manifest declaring two page entries with distinct keys
   and the same route installed cleanly, empirically confirmed by the
   reviewer with a scratch test (`err=nil`, 2 dispatchable rows at the
   same route). The #472 precedent could defer the equivalent
   within-manifest *key* case because `plugin_entries` has a real
   `UNIQUE(plugin_id, key)` constraint; there is no equivalent constraint
   or index on `route`, so that rationale doesn't transfer. **Fix:** a
   `seenRoutes` map in `validatePageEntryRoutes`, exactly mirroring
   `validatePaymentEntryKeys`'s existing `seenKeys`/`seenLabels` pattern
   (already in this same file, added by ut-docs#168 for the identical
   no-DB-constraint reason). New test:
   `TestPersistManifest_RejectsDuplicateRouteWithinManifest` — TDD-
   verified (reverting just the `seenRoutes` block makes it fail with the
   claimed "accepted two page entries... sharing a route" message).
2. **The new route check silently masked the two pre-existing
   #472 key-collision regression tests.** Both used the `pageManifest`
   helper's default `route = "/"+key`, so the two colliding plugins in
   `TestPersistManifest_RejectsPageKeyOwnedByAnotherPlugin` and
   `TestRollback_RejectsCollidingPageKeys` collided on *route* as well as
   *key* — the reviewer empirically confirmed both tests kept passing
   with `validatePageEntryKeys` stubbed out entirely, because the route
   check's error message happened to satisfy the same string assertions
   (`Contains("shared")`, `Contains("com.first.page")`). **Fix:** both
   tests now use `pageManifestRoute` with distinct routes per plugin, so
   only the key check can be what rejects them. Re-verified with the same
   stub-and-confirm-fail technique the reviewer used, in both directions
   (`PersistManifest` and `Rollback`) — both now correctly fail when
   `validatePageEntryKeys` is disabled.

### Nitpicks/informational — accepted as-is, not fixed

3. The end-to-end `internal/pages` dispatch test's plugin IDs
   (`com.first.route`/`com.second.route`) happen to sort correctly
   regardless of the fix, since `ListPageEntries` orders by
   `(sort_order, plugin_id, key)` — the install-rejection assertion fires
   first either way, so this specific test can't itself distinguish a
   dispatch-ordering regression. The row-leak case that assertion is
   meant to guard is independently, genuinely caught by
   `page_route_validation_test.go`'s `routeCount == 1` check. Left as-is;
   not worth the churn of renaming plugin IDs to force a specific sort
   order in a test that already has a real detector elsewhere.
4. No route-format hygiene (empty-after-trim/whitespace/`':'`) the way
   the key check has — asymmetric with the precedent, but a malformed
   route just makes the page permanently undispatchable (self-inflicted,
   no collision, no data risk), not a new bug class. Candidate follow-up,
   not blocking.
5. `ClassifyInstallError`'s two cases are message-substring-scoped
   (`"page entry key"` vs `"page entry route"`) and disjoint for every
   real message either check can produce (verified by table tests in
   both directions) — a contrived route string containing the literal
   text "page entry key" would misroute to the wrong (but equally
   non-retryable) translated message. Not worth guarding against
   attacker-chosen manifest text for a cosmetic wording difference.
6. A disabled-but-installed plugin's route still blocks a new
   registration (`FindPageRouteConflicts` has no `is_active` filter,
   unlike `ListPageEntries`) — the same accepted gap as the #472 record's
   own nitpick 5 for keys (defensible: re-enabling would recreate the
   collision), not a new problem introduced here.

## Verified beyond the automated suite

- **TDD, both fixes, both directions.** Dev wrote the original four
  route tests red-then-green against the initial implementation
  (revert-fix-files → confirm both new PersistManifest/Rollback tests
  fail with the claimed errors → restore → confirm green). After the
  review's two findings were folded in, re-ran the same
  revert→confirm-fail→restore→confirm-pass cycle personally for both:
  the `seenRoutes` block (confirms
  `TestPersistManifest_RejectsDuplicateRouteWithinManifest` is
  load-bearing) and the two updated regression tests (confirmed both now
  correctly fail — not pass — when `validatePageEntryKeys` is stubbed
  out, proving they test the key check again, not the route check).
- **Residue check**: a rejected install leaves zero rows in `plugins`
  and `plugin_catalog` (asserted directly in the new tests, same pattern
  as #472's `plugin_entries`/`plugin_catalog` checks).
- **Dispatch-survival check, through the real HTTP handler**: the new
  `internal/pages` test installs two colliding-route plugins via the real
  `plugins.PersistManifest` against a fully-migrated schema
  (`openRealSchemaPagesDB`), serves `GET /plugin/<route>` through
  `registerPluginPages`'s actual mux, and asserts the response body names
  the first (accepted) plugin and never the second (rejected) one — not
  just a DB-row check.
- **Cross-type false-positive check inherited from #472's pattern**: the
  route check only ever looks at `type='page'` rows (same `WHERE`
  clause shape as the key check), so a route string coincidentally
  matching an unrelated entry type's field never conflicts.
- **Complete write-path coverage**, independently re-confirmed by the
  reviewer: `ReplacePluginEntries` has exactly two production callers
  (`manifest.go`, `rollback.go`), and both now call
  `validatePageEntryRoutes` — no bypass path.
- **Translations**: `plugins.install.error.page_route_conflict` was
  added to `fa`/`tr`/`ar` by structurally adapting the already-shipped,
  reviewed `page_conflict` string for the same UI surface (substituting
  "route" for "key" in each language) rather than a fresh model
  round-trip — the self-hosted translation endpoint
  (`http://192.168.1.231:11434`, `reference/translation.md`) is
  unreachable from this cloud pipeline session, same as #472's own note.
  Flagged here rather than silently guessed; worth a native-speaker QA
  pass when someone with LAN access is next in the area.
- Full `go build ./...`, `go vet ./...`, `gofmt -l` (clean on every file
  this diff touches — 4 pre-existing drifted files elsewhere in the repo
  are unrelated, tracked separately as ut-docs#318), full
  `go test ./... -race` (all green, no pre-existing failures reproduced;
  reviewer ran the full suite, Dev re-ran the `plugins`/`pages`/`data`
  packages after folding in the review's fixes), and all 4 CLAUDE.md
  guards (`guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`) — run by both Dev and the
  reviewer independently.
- No real client/shop name anywhere in tests (fictional plugin IDs
  only); no secret-shaped literal (`https://example.invalid` per RFC
  2606, `deadbeef` as a fake hash, both inherited from the #472
  precedent's own test fixtures).

## Deferred / explicitly out of scope

- Route-format hygiene (nitpick 4 above) — candidate follow-up.
- The disabled-plugin-reserves-its-route gap (nitpick 6) — same accepted
  shape as the #472 precedent's equivalent key-side gap.

## Safe-to-merge verdict

Yes, after both should-fix items were folded in and re-verified
(including fresh TDD revert-checks on each) by Dev personally, without a
second full independent review round — neither finding was
money/tax/data-loss/security class, and both were small, mechanical,
precedent-matching fixes (a `seen`-map pattern already used three times
elsewhere in the same file, and a test-fixture route change). No real
client/shop name used anywhere; no secret-shaped literal introduced. No
manual/`web/help/` update needed — confirmed directly (grepped
`web/help/en/plugins.md` for conflict/fail/error/retry language; found
none) rather than assumed from the #472 precedent alone — this change has
no user-visible surface (install-time validation only, no new
template/page/route).
