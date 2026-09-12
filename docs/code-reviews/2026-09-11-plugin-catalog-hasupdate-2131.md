# Code review: plugins page can't tell "unknown" from "current" (ut-docs#2131)

**Card:** universaltill/ut-docs#2131 — "Till silently reports a plugin
current when it has no version to compare against (hasUpdate can never
become true)"
**Complexity:** medium (Dev: inline, session model Sonnet; Review: Opus
subagent, fresh context, isolated worktree, independent from the Dev pass)

## The bug — three independent root causes

1. `/plugins`' management page (`internal/pages/plugins_page.go`) matched
   an installed plugin to the marketplace catalog only via the
   `plugin_install_status` listing mapping. A plugin installed via
   "Import from file" writes no such row at all
   (`internal/data/sync_plugins_repo.go`'s own doc comment), so it could
   never resolve a catalog match — `latest` stayed `""` forever,
   indistinguishable from "up to date". Observed on the pilot tablet:
   `language-de` sat 40 releases behind showing `hasUpdate: false`.
2. `CatalogRepository.GetOrFetch` (`internal/plugins/marketplace/catalog_repository.go`)
   computed staleness correctly (`Get()`'s 15-minute window) and then
   ignored it — once any snapshot ever landed on disk, `GetOrFetch` served
   it forever, however old.
3. A locale-filtered catalog snapshot excluding language packs — already
   fixed independently via `ut-cloud` PR #133, not part of this diff.

## What shipped

- New `internal/plugins/catalog_match.go`: extracts the two-tier
  byListing/byAuthorName match `UpdateChecker.CheckForUpdates` already had
  (ut-docs#1953) into `CatalogIndex`/`IndexCatalog`/`Resolve`, so both call
  sites use the same logic instead of silently drifting (which is exactly
  what had happened: `update_checker.go` had the fallback, `plugins_page.go`
  didn't). Also adds `VersionNewer`, a real semver-ish comparison
  (`compareVersions`) instead of `plugins_page.go`'s old naive `!=` check.
- `internal/data/plugin_repo.go`: `ManagedPluginRow` gains `Author`
  (`COALESCE(author, '')`) so the management page has what it needs to
  attempt the author+name fallback.
- `internal/pages/plugins_page.go`: builds a `plugins.CatalogIndex` from
  the fetched snapshot, resolves each row via
  `idx.Resolve(listingByPlugin[row.ID], row.Author, row.Name)`. Adds
  `versionUnknown` to the JSON payload — true whenever nothing usable was
  resolved, so `latest:""` is never rendered as "current" (issue's AC2).
- `internal/plugins/marketplace/catalog_repository.go`: `GetOrFetch`
  refetches when `Get()` reports the cache stale, falling back to the
  stale snapshot if the refetch itself fails (offline-first preserved).
  `Fetch` also gained a nil-`client` guard (see review finding below).
- `internal/pages/plugin_api.go`: `applyPluginUpdate` gains
  `resolveListingViaCatalog`, the same author+name fallback, so the
  "Update" button installed plugins get from the badge above can actually
  install (see review finding below — this was missing from the first
  draft).
- `web/ui/pages/plugins.html` + `web/locales/{en,ar,fa,tr}.json`: a new
  `versionUnknown`-gated tag, mutually exclusive with the existing
  update-available one. `de`/`es` UI-string packs are external repos
  needing the self-hosted NAS translation model — `blocked:env` follow-up,
  same standing convention as this repo's other recent cards (#1076, #1093).
- `web/help/{en,ar,de,fa,tr}/plugins.md`: a new manual item, in all five
  locales that ship in this repo (unlike the UI-string JSON, `de`'s manual
  ships directly here, not via an external pack). `ar`/`fa`/`tr` already
  carried a pre-existing, tracked drift on this topic (ut-docs#1962); the
  baseline entries were updated (not silently re-recorded) since both
  sides of that gap moved together by one.
- `make docs-shots` re-run for real (headless Chromium) after every round
  of code changes that touched the app surface, twice in total.

## Independent review (Opus, fresh context, isolated worktree)

Spawned via `Agent` with `model: opus`, pointed at its own git worktree of
the pre-review WIP commit — never touched the orchestrator's shared
checkout. Verified independently, not just re-stated: full `gofmt`/`go
build`/`go vet`/`golangci-lint`/`go test ./...` (plus a `-race` run on
`marketplace`), every relevant CI-blocking guard, and TDD re-verification
via a real revert→fail→restore→pass cycle for all four new/changed test
pairs. Initial verdict: **not safe to merge** — two blockers, one
correctness-adjacent finding fixed alongside them, one false-pass test,
two accepted follow-ups.

**Blockers, both fixed here:**

1. **A dead-end "Update" button.** The badge fix (above) made `hasUpdate`
   true for a file-imported plugin resolved via author+name — but
   `applyPluginUpdate` still hard-required a `plugin_install_status` row
   and returned `ErrPluginUpdateNoListing` otherwise, so the button 404'd
   with a non-localized message on every click for exactly the population
   this card targeted. Fixed: `applyPluginUpdate` now tries
   `resolveListingViaCatalog` (same `IndexCatalog`/`Resolve` fallback,
   looking up the plugin's author via `ListInstalledPlugins`) before
   giving up. New test:
   `TestApplyPluginUpdate_NoListingButCatalogMatchFound_PastNoListingGate`
   (`internal/pages/plugin_api_test.go`) — asserts the call gets past
   `ErrPluginUpdateNoListing` (it still fails later, since there's no real
   download-token/artifact server behind the fake catalog endpoint — that's
   the existing install pipeline's own concern). TDD-verified: reverting
   `plugin_api.go`'s change alone reproduces `ErrPluginUpdateNoListing`
   exactly.
2. **AC2 wasn't fully closed.** A catalog match with an empty `Version`
   (e.g. a listing the marketplace hasn't populated a version for yet)
   still fell through as neither `hasUpdate` (rejected by `VersionNewer`'s
   empty-candidate guard) nor `versionUnknown` (a match *was* found) —
   rendering with no chip at all, i.e. `latest:""` presented as current,
   the exact bug class the issue is named after, reached by a second
   route. Fixed: `plugins_page.go` now requires `catalogPlugin.Version !=
   ""` too before treating a match as usable. New test:
   `TestPluginsPage_MatchWithEmptyVersionReportsVersionUnknownNotCurrent`.
   TDD-verified.

**Also fixed alongside the blockers (raised as a review question, closed
as a real finding):**

3. **Cross-vendor name-collision risk in the author+name fallback.**
   `plugins.author` is nullable (`COALESCE(author, '')`), and a catalog
   listing's `developer_id` can likewise be empty — matching on name alone
   would treat two unrelated vendors' same-named plugin as the same one,
   and (via finding 1's fix) could point the "Update" button at an
   entirely different vendor's listing. The original `ut-docs#1953`
   heuristic this mirrors already carried an explicit warning about this;
   the extraction into `catalog_match.go` had dropped that comment.
   `IndexCatalog` now never indexes a listing with an empty
   `DeveloperID` for the author+name tier, and `Resolve` now refuses the
   fallback outright when the installed side's author is empty too — not
   a "guaranteed safe" fix (two vendors could still coincidentally share
   both fields), but the cheap, meaningful narrowing the review named.
   New test: `TestCatalogIndexResolve_NeverMatchesOnNameAloneWithEmptyAuthor`
   (`internal/plugins/catalog_match_test.go`). TDD-verified.

**Test-quality finding, fixed:**

4. `TestGetOrFetchFallsBackToStaleCacheWhenRefetchFails`'s first draft
   closed the server before forcing staleness, so a refetch *attempt*
   was indistinguishable from *no attempt* — `hits` stayed at 1 either
   way, and the test passed unchanged against the pre-fix `GetOrFetch`
   (which never called `Fetch` at all when a cache existed). Fixed by
   serving a real HTTP 500 on the simulated failure instead: `hits` only
   reaches 2 if a refetch was genuinely attempted, which is what the test
   claims to verify.

**Accepted as legitimate follow-ups, not fixed here (both explicitly
scoped out by the review's own verdict):**

5. `/plugins` now makes a real network call (up to the marketplace
   client's 30s timeout) inline in the page handler whenever the cache is
   stale, serialized behind `CatalogRepository`'s write lock with no
   double-check after acquiring it — a till on a dead-end network route
   could see `/plugins` hang for up to 30s, and N concurrent loads become
   N sequential round-trips. Not a checkout-path violation (no stated
   non-negotiable is broken), but contrary to the offline-first spirit for
   an admin surface. No lock-ordering/re-entrancy defect (`-race` clean).
   Filed as a follow-up rather than redesigning `GetOrFetch` into an
   async-refresh pattern within this card's scope.
6. The `update_unknown_hint` copy originally blamed "not in the catalog
   data" specifically — inaccurate when the real cause is "marketplace
   not configured" or "fetch failed", both of which also set
   `versionUnknown` for every plugin. Fixed by rewording the `en/ar/fa/tr`
   hint (and the manual line already described the feature generically
   enough not to need a change) to state the effect ("we can't tell")
   rather than guessing a specific cause.

**Also verified, no defect found:**

- The nil-`client` guard added to `Fetch` (`catalog_repository.go`) is
  purely defensive: the review confirmed the single production
  construction site always passes a real client, and the four nil-client
  callers are all test fixtures that pre-date this diff — the guard exists
  because those fixtures' hand-written snapshots have a zero-value
  `FetchedAt`, which the pre-fix `GetOrFetch` never actually reached
  `Fetch` for (staleness was always ignored), so this diff's own fix newly
  exposed a latent nil-pointer panic (confirmed live:
  `TestStorePageShowsLifecycleStatusAndInstalledSplit` panicked before this
  guard was added). Cleaner long-term might be updating those fixtures to
  use a real `testClient(...)` instead of enshrining nil-client as
  supported — noted, not changed here (out of this card's scope).
- Screenshot diffs beyond the four expected `plugins.png` files
  (`ar/catalog.png`, `en/catalog.png`, `ar/sell.png` across the two
  `make docs-shots` runs) were pixel-compared against `main`: every
  differing pixel sits on an antialiased edge of an already-present
  element (a badge edge, a focus ring), zero layout shift — incidental
  rasterization non-determinism between runs, not a regression.
- Money/data-access/RTL: clean. No money involved; all new SQL is in
  `internal/data`; the new `<span class="tag">` uses no physical
  left/right properties.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...`, `golangci-lint run
  ./...` (0 issues) — clean on the final tree, checked by both the Dev
  pass and the independent review.
- `go test ./...` (whole module) green — re-run after every fix round,
  including a `-race` run on `internal/plugins/marketplace` specifically.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` (three pre-existing
  baseline entries updated, not newly introduced — verified by running the
  guard and confirming it emits exactly the recorded signatures),
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`, `guard-page-http-error.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh` — all green on the
  final tree.
- `guard-shellcheck-version.sh` fails in this cloud sandbox (`no
  'shellcheck' binary found on PATH`) — confirmed pre-existing and
  unrelated to this diff by reproducing the identical failure on a clean
  `main` checkout with no changes stashed in. CI's own `ubuntu-latest`
  runner ships shellcheck preinstalled; this is a sandbox gap, not a
  regression.
- Independent TDD re-verification (revert → fail with the exact claimed
  signature → restore → pass), performed by the review subagent for every
  one of the six new/changed regression tests across this diff and its
  fix round, not just asserted by the Dev pass.

---
_Generated by [Claude Code](https://claude.ai/code)_
