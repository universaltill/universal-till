# Code review: `internal/data` deadcode-baseline slice (ut-docs#1566)

**Date:** 2026-09-12
**Branch:** `fix/1566-deadcode-internal-data-slice`
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in
universal-till"), this cycle's per-package slice: `internal/data` (9 of the
78 remaining `scripts/ci/deadcode-baseline.txt` entries — `plugin_repo.go`
x8, `related_items_repo.go` x1).
**Complexity:** hard (card-level; this slice's actual diff is doc comments
only, no behaviour change).
**Review model:** Opus (independent subagent, fresh context), per
`MODEL-ROUTING.md`'s hard-tier routing.

## What this slice does

Each of the 9 `internal/data` baseline entries was individually
investigated (repo-wide caller search across production code, tests,
`scripts/`, `e2e/`) rather than deleted on sight. None were safe to delete
this slice — every one turned out to be either a legitimate test-fixture
helper, a deliberate narrower sibling of an already-used method, half of a
dead cross-package call chain (can't be removed without also touching
`internal/plugins`, out of this PR's package scope), or a case with its own
dedicated test proving intentional semantics distinct from a similar live
method. Doc comments were added to each explaining its resolution — same
shape as the card's own `internal/httpx` slice
(`docs/code-reviews/2026-09-12-deadcode-httpx-slice-1566.md`,
universal-till#1148), where 5 of 7 entries also needed no code change, just
documentation.

Baseline file (`scripts/ci/deadcode-baseline.txt`) is unchanged: no entries
added or removed.

Two follow-up cards were filed for genuine gaps found along the way:
- universaltill/ut-docs#2225 — the plugin-revocation read-back chain
  (`GetRevokedPlugins`/`ListRevokedPlugins`) is dead, though enforcement
  itself is live (see below).
- universaltill/ut-docs#2226 — `HasActivePrinterPermission` has no caller
  despite a differential test proving intended distinct semantics from
  `HasActivePrinterCapability`.

## Independent review — Opus (fresh subagent)

**First pass: changes requested.** One blocker, one major, two minor
factual errors in the added prose:

- **BLOCKER — `ListRevokedPlugins` comment inverted the real state of
  revocation enforcement.** The first draft claimed "the whole
  revocation-checking chain... currently never runs in the product." Wrong:
  `internal/server` wires `RevocationChecker` and runs `SyncRevocations` on
  a 30-minute ticker whenever a marketplace endpoint is configured;
  `SyncRevocations` disables a revoked plugin directly via
  `PluginRepo.GetPlugin`/`SetPluginState`, never through
  `ListRevokedPlugins`. Only the separate read-back accessor,
  `RevocationChecker.GetRevokedPlugins` (itself unreachable, and
  `ListRevokedPlugins`'s only in-tree caller), is actually dead. This also
  invalidated ut-docs#2225 as originally filed (it asked a BA/security pass
  to choose between "real security gap" and "abandoned approach" — both
  wrong; enforcement was never in question).
- **MAJOR — `GetPluginVersionAt` comment said "never wired."** Wrong: it
  was superseded by the batched `GetPluginVersionsAt` (ut-docs#1323,
  documented three lines below it in the same file), which replaced its one
  real production caller (`loadReceiptLegalBlocks` on the tender path).
- **Minor — `InstallPlugin`**: cited "six... internal/data/internal/pages/
  internal/plugins test files"; actually seven, all under `internal/data`
  (a substring grep had picked up an unrelated `cloudInstallPlugin`
  function in `internal/pages` tests, and missed one `internal/data` file
  that calls it with `context.Background()` instead of a `ctx` variable).
- **Minor — `LastRebuilt`**: cited `internal/pages/suggestions_api.go` as
  `Rebuild()`'s live caller; that file only calls `SuggestForBasket`. The
  real caller is `internal/server` (startup + 24h ticker).
- **Nit** — "unreachable from either shipped binary" (used twice): the
  deadcode guard actually analyzes three roots (`.`,
  `./cmd/unitill-desktop`, `./cmd/unitill-uninstall`). Softened to avoid
  the specific miscount.

**Confirmed correct by the reviewer, no changes needed:** the keep/no-delete
disposition for all 9 entries (including the cross-package-scope reasoning
for `ListRevokedPlugins`/`CatalogPage`); `UpsertPluginSetting` and
`DeleteStorageKey` correctly left untouched (already had adequate prior
comments); `DeleteStorage`'s ADR-0072/fiscal-retention reasoning;
`HasActivePrinterPermission`'s differential-test reasoning; `CatalogPage`'s
production-path claim; ut-docs#2226's accuracy; no `CLAUDE.md` rule
violated (no SQL outside `internal/data`, no money/i18n/RTL/offline-first/
kiosk-engine/help-topic surface touched); comment-only diff verified
mechanically (`git diff -U0` filtered for non-comment-line changes: empty).

**Fixes applied** (second commit, same branch): reworded the
`ListRevokedPlugins`, `GetPluginVersionAt`, `InstallPlugin`, `LastRebuilt`,
and `CatalogPage` comments to state the corrected facts above; re-filed
ut-docs#2225 without the inverted premise and dropped its `security` label
(confirmed not a security gap); left `GetPluginVersionAt` in place rather
than attempting a same-PR deletion (a clean deletion needs re-pointing
`internal/pages/ui_smoke_test.go`'s `ut-docs#625` schema-regression test at
the batched form first — left as a note for a future `internal/data` slice
rather than bundled into this comment-only PR).

## Verified beyond automated tests

- Read `internal/server/server.go` directly to confirm the revocation
  ticker and `RelatedItemsRepo.Rebuild` startup/ticker wiring (both cited
  incorrectly in the first draft, now corrected).
- Read `internal/plugins/revocation.go`'s `SyncRevocations`/
  `processRevocation` to confirm it never calls `ListRevokedPlugins`.
- Read `internal/pages/pos_api.go`'s `GetPluginVersionsAt` call site and
  its own doc comment (ut-docs#1323) to confirm the supersession history.
- Re-ran the precise caller grep for `InstallPlugin` that undercounted in
  the first draft, confirming seven real callers, all `internal/data`.

## Gate (re-run after the fix commit)

```
gofmt -l internal/data/plugin_repo.go internal/data/related_items_repo.go   clean
go build ./...                                                               OK
go vet ./...                                                                 OK
go test ./internal/data/...                                                  ok (82.7s)
golangci-lint run ./internal/data/...                                       0 issues
```

`scripts/ci/guard-deadcode-baseline.sh` could not be run in this sandbox
(requires GTK/WebKit dev headers for the `desktop`-tagged whole-program
analysis; `apt-get install libgtk-3-dev libwebkit2gtk-4.1-dev` failed with
mirror 404s in this environment). Not a risk for this diff specifically —
it's comment-only, and `deadcode`'s reachability analysis is unaffected by
comments — but real CI (which does have the headers) is the actual gate for
this guard; watched via the PR's CI run rather than asserted locally.

## Full `go test ./...`

Run once, separately, before the review fixes (comment-only changes since
then don't affect test outcomes): clean, all packages passing.
