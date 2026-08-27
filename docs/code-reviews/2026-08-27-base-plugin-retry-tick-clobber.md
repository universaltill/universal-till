# basePluginRetryTick: stop wholesale-clobbering the pending list (ut-docs#1117)

**Card:** universaltill/ut-docs#1117 — follow-up from the ut-docs#1110 review
(`docs/code-reviews/2026-08-26-setup-language-catalog-followup-1110.md`,
finding 6), which fixed the same clobber pattern in
`installBasePluginsForSetup` but explicitly deferred this narrower,
first-boot-only instance.

**Complexity:** easy. Dev inline (Sonnet), review at Sonnet (fresh context,
isolated worktree — the complexity:easy exception where "different model"
relaxes to "different instance").

## What shipped

`basePluginRetryTick` (`internal/pages/setup_base_plugins.go`) is the
5-minute background retry loop for free base plugins (e.g. language packs)
queued during first-boot setup. It used to: load the pending list, attempt
each spec against the marketplace (a real network round trip per spec),
then wholesale-replace the persisted list with whatever was left —
`savePendingBasePlugins(ctx, d, remaining)`. Any spec another writer queued
(e.g. a concurrent `POST /api/setup` request landing right around the
tick's own first fire, ~30s after boot) *during* one of those round trips
was silently dropped, because the tick's write overwrote the list with its
own now-stale snapshot.

Fix: `basePluginRetryTick` now collects only the specs that actually
installed this pass (`installed`) and removes exactly those via a new
`removePendingBasePlugins` helper, which re-reads the persisted list fresh
immediately before removing — so anything queued mid-tick by another
writer survives untouched. Mirrors the `addPendingBasePlugins`/
`dismissPendingBasePlugin` merge-safe-write pattern #1110 already
established on the add/single-remove side; `removePendingBasePlugins` is
the batched-remove sibling of `dismissPendingBasePlugin`.

## Independent review (Sonnet, fresh context, isolated worktree)

Verdict: **safe to merge**, no blocking findings.

- **TDD claim independently re-verified**: reverted `basePluginRetryTick`
  to the old wholesale-replace pattern, re-ran
  `TestBasePluginRetryTick_DoesNotClobberConcurrentlyQueuedSpec` — failed
  with the exact predicted symptom (`expected the concurrently-queued es
  spec to survive the tick's write untouched, got []`, i.e. it was wiped
  entirely). Restored the fix, confirmed green again, confirmed no
  leftover diff.
- **Verified correct**: `removePendingBasePlugins`'s re-read-then-remove
  logic (dedupes naturally via the drop-set, no-ops when nothing actually
  matched); the idempotent-already-active install path still correctly
  lands in `installed` (no behavior change from before — both a fresh
  install and a no-op "already active" return `nil` from
  `resolveAndInstallBasePlugin`); the new `onCatalogRequest` test hook on
  `fakeMarketplace` (mutex-guarded read, invoked outside the lock, nil by
  default — every pre-existing test that doesn't set it is unaffected);
  the new test is fully deterministic (the concurrent write is injected
  synchronously inside the fake HTTP handler via the hook, not raced
  against a real goroutine/sleep); doc comments match what the code does;
  no `os.MkdirAll`/cwd-path issues, no real client/shop names, no
  secret-shaped literals.
- **CLAUDE.md compliance confirmed**: no SQL added outside
  `internal/data`/`internal/db` (pre-existing SQL-shaped lines the
  reviewer's grep hit are in `_test.go` files, already exempt from
  `guard-data-access.sh`); no i18n/UI/money surface touched — none of the
  binding rules are implicated by this diff.
- **Full `internal/pages` package with `-race` timed out (600s and again
  at 1200s) — confirmed NOT a deadlock**, and not a regression from this
  diff: goroutine dumps at both timeouts showed every goroutine either
  idle in `connectionOpener`'s select or actively `[runnable]` inside real
  SQLite migration/parsing work. The same full package **without `-race`
  passes cleanly in 88s, 1426/1426**. `-race` instrumentation on a package
  this size — most tests spin up a real migrated SQLite DB plus real
  HTTP/Ed25519 crypto — plausibly exceeds any sandbox timeout regardless
  of this diff; recorded here so a future reviewer doesn't re-chase it as
  a regression this change introduced. (The dev-side gate ran the same
  full package `go test ./...` without `-race`, plus a targeted
  `-race` run scoped to the touched tests only, both clean — see below.)

## Verification beyond automated tests

- Dev-side: reverted the fix locally myself first, confirmed the new test
  failed with the same "wiped to `[]`" symptom, restored, confirmed green
  — before ever handing off to review. The reviewer's independent
  revert-then-restore above reproduced the identical result from a cold
  read of the diff.
- `gofmt -l`, `go build ./...`, `go vet ./...` — clean.
- Targeted `-race` run (`TestBasePluginRetryTick|TestInstallBasePluginsForSetup|
  TestResolveAndInstallBasePlugin|TestSetupWizardDE`) — clean, no data race
  on the shared pending-list read/write path or the new test hook.
- Full `go test ./...` (whole module, no `-race`) — clean, all packages.
- All CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list — clean, including `guard-docs-shots.sh` after
  `make docs-shots` regenerated the manifest (no visible UI surface
  changed by this diff — background retry logic only — so the manifest's
  hash moved with no PNG content change).

## Not verified / accepted gap

- No real-browser drive: this fix changes background server-side retry
  logic only, with no new markup and no new visible state — the existing
  Settings pending-plugin list view (`pendingBasePluginViews`) is
  unchanged by this diff.

## Safe-to-merge verdict

Yes. No fixes requested by review; nothing to apply.
