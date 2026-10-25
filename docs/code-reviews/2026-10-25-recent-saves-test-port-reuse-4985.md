# Review: flaky TestCatalogWriteThrough_DesignerThirdSaveIsNotASelfConflict (ut-docs#4985)

Branch `fix/4985-recent-saves-test-isolation`. Author: Sonnet 5.5 (Dev
subagent; lane:cloud-41, complexity:medium). Root cause and design: Opus 5.5
(orchestrator). Independent review: Opus 5.5, fresh context, in a detached
worktree.

## What happened

- CI `pages-shuffle` (seed 1791386291628608919, universal-till#2141) failed
  at `catalog_base_stamp_3606_test.go:236`: `save 1 = 409`. The first
  Designer save with the main till's own current stamp was refused as a
  conflict.

## Root cause (reproduced deterministically)

- `catalogsync.recentSaves` (ut-docs#3606) is a package-global map. It is
  keyed by main-till URL + kind + id and has a 10-minute TTL. Its doc comment
  said each test pair runs on its own httptest server, so tests never share
  an entry. That is false: after a server closes, the kernel hands its
  `127.0.0.1:PORT` to a later server.
- Every pair also seeds `cat1` with the same stamp, `2026-01-01 08:00:00`.
  Suppose an earlier test (in shuffled order) saved `cat1` through a server
  whose port a later pair then gets. `recentSaves.base` finds `s0` in that
  stale chain and sends the earlier test's main-till stamp as the base. The
  fresh main till refuses it.
- To reproduce: a subtest pair saves `cat1`. Then a new pair listens on the
  same address and saves with the seeded stamp. That save gets 409 every
  time. The test's one-second sleeps were not the cause.
- Production impact: none in practice. A main till is one stable URL. Even
  a different database behind the same address within 10 minutes would
  cause a spurious refusal (the safe direction), never an overwrite.

## What shipped (test-only, plus a comment)

- `newCatalogWriteThroughPairOn(t, ln)` (`catalog_write_through_test.go`).
  Each pair's main-till URL gets a unique `/pair-N` path prefix, which the
  test server strips. A reused port therefore never shares `recentSaves`
  entries. `newCatalogWriteThroughPair` wraps it with an ephemeral listener.
- Regression test `TestCatalogWriteThrough_PairAtAReusedAddressStartsClean`
  builds two pairs on the same listener address. The second pair's first
  save must be 204. If the address can't be re-listened, the test fails; it
  never skips.
- The `recentSaves` doc comment in `catalogsync/forward.go` now says what
  is true.
- First attempt, dropped: an exported production hook,
  `catalogsync.ForgetMainTillForTests`, called on cleanup.
  `guard-deadcode-baseline-growth.sh` (ut-docs#3406) refuses a change that
  baselines its own new unreachable function, and the URL prefix needs no
  production code.

## Findings (review)

- None blocking. Verdict: **safe to merge**.
- Low, accepted: the regression test fails if another process takes the
  freed port before `net.Listen`. That is very rare, and failing is better
  than skipping.
- Low, pre-existing, filed as a Backlog card: `deadPrimaryURL()`
  (`held_sale_sync_proxy_test.go`) returns a closed server's address. A
  later live server can take that port, and the "unreachable" tests would
  then get a 404 instead of a connection error.
- The reviewer checked the rest of the package for this pattern. In
  `internal/pages`, no other test gives a replica a live main till. The
  `pages/catalog` stub main till never returns `entity_id`/`updated_at`, so
  `remember` never runs there. catalogsync starts no goroutines.

## Verified

- TDD, re-verified independently in a separate worktree. With the prefix
  forced to `""`, the test fails with `first save on a fresh pair at a reused
  address = 409, want 204`. With the prefix, it passes 20 times out of 20
  (`-count=20`).
- `go test -count=2 -race -shuffle=on -run
  'CatalogWriteThrough|BaseStamp|SyncCatalog' ./internal/pages` passes, and
  so does `-count=15 -shuffle=on`.
- `go build ./...`, `go vet ./internal/pages/...` and `gofmt -l .` are
  clean. Every `ci.yml` guard passes locally except two this container
  can't run, left to CI: `guard-deadcode-baseline.sh` (the local `deadcode`
  binary was built with go1.26) and `guard-shellcheck-version.sh` (no
  shellcheck).
- `go test ./...` failed once, in an unrelated package:
  `cloudsync.TestStockUploadLoopDebouncesOnTheRealtimeTier`. That run
  overlapped the reviewer's race runs. The test passes 10 times out of 10 on
  its own, and it is filed as a Backlog flake card.
