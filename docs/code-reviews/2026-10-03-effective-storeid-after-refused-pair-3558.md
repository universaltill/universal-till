# Review: Effective() store id after a refused "Pair with a shop" (ut-docs#3558)

**Date:** 2026-10-03 · **Branch:** `fix/3558-effective-storeid-after-refused-pair`
**Built by:** Sonnet (complexity:easy) · **Reviewed by:** Opus 5.5, fresh context

## What shipped

- `internal/enroll/enroll.go`: `Effective()` and `currentStoreAuth()` now let the
  live store id win over Init's startup copy whenever Pair replaced the identity
  (`(cur.StoreID != "" || identityReplaced) && !storeIDExplicit`). A refused pair
  on a main/standalone till clears `cur.StoreID`, so both now report `""`
  instead of the stale startup store — the same rule the device id, merchant id
  and token already followed (#3523).
- `internal/enroll/pair_test.go`: `TestPairRefusedLeavesIdentityCleared` asserts
  both functions report an empty store id after a refused pair;
  `TestPairReplicaMatchingStore` asserts a replica keeps its synced store.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A replica that pairs before its first admin sync (`cur.StoreID` empty) now reports `""` instead of the store-name placeholder after a refused pair. | Accepted: its token is empty too, so every cloud path is already gated; `""` is the honest value. |
| 2 | nit | The replica assertion also passes pre-fix (`cur.StoreID` is non-empty there). | Accepted: it's a guard against future regressions; the refused-pair test carries the TDD proof. |
| 3 | nit | WIP commit subject. | Fixed: squashed into a real commit. |

Consumers checked (cloudsync, alerts, pages/cloud_link, setup_tse, plugin
installer, device registration): all already require a store id **and** a
token; after a refused pair the token and merchant id were already empty, so
nothing new fails. The plugin installer now refuses an empty store id locally
instead of sending the stale one with an empty bearer.

## Verified

- TDD re-verified independently in an isolated worktree: reverting `enroll.go`
  fails `TestPairRefusedLeavesIdentityCleared` with
  `Effective store id = "store-old" after a refused pair, want ""`; reverting
  only the `currentStoreAuth` hunk fails the second assertion; restored → pass.
- `go vet`, `gofmt -l`, `go test -race ./internal/enroll/`, consumer packages,
  full `go test ./...`, data-access/i18n/core-neutral/kiosk guards,
  `golangci-lint` on the package.
- No UI surface touched; no user-manual change needed (backend-only, no
  visible behaviour change on a healthy till).

**Verdict:** safe to merge. No deferred items.
