# Test coverage batch 11: sync API/assets — found a real LAN image-sync regression

2026-07-29

Continuing `internal/pages` handler coverage: the rest of the sync stack —
`sync_api.go` (QR pairing: enrol-token, enrol, ping, revoke, promote,
join, the Tills page) and `sync_assets.go` (primary→replica item-image
sync). `sync_sales.go` was covered in batch 10.

## Real regression found: LAN image sync has been silently dead

`sync_assets.go` had `const assetsRoot = "web/public/assets/items"` — a
cwd-relative path. Earlier this same session (batch: the very first fix
tonight, `edf8fe4`), item image uploads were moved from that exact
cwd-relative path to `paths.Data("public", "assets", "items")` (a stable
per-user data dir), because uploads were being silently lost on every app
self-update. That fix updated the three write sites in
`catalog/handlers.go` but never touched `sync_assets.go`'s `assetsRoot`,
which backs the primary→replica item-image sync manifest
(`GET /api/sync/assets`) and file server (`GET /api/sync/assets/file`).
Since nothing has written to the old path since that earlier fix,
`listItemAssets()` has been walking an empty/dead directory — replica
tills have been silently receiving zero item images via LAN sync, with no
error anywhere to surface it. This is a genuine, if quiet, production
regression this session's own earlier fix introduced.

**Fix**: `assetsRoot` is now `func assetsRoot() string { return
paths.Data("public", "assets", "items") }`, matching the write path
exactly. Three call sites updated.

**Caught by**: a new regression test (`sync_assets_test.go`) that writes
a file where uploads actually land and asserts `listItemAssets()` finds
it — confirmed to fail against the pre-fix constant and pass with the
fix.

## Independent review (opus) — thorough on the security-relevant path

Given this touches `safeAssetPath` (the traversal guard on a path
reachable by any enrolled replica's bearer token), the review specifically
hunted for bypasses beyond what the tests enumerate: URL-encoding, Unicode
look-alikes, double-encoding, symlink-following. None found reachable —
`safeAssetPath`'s combination of substring rejection (`..`, leading `/`,
backslash) plus `filepath.Clean` + `HasPrefix("..")` + `IsAbs` checks
holds, and the replica independently re-validates every manifest path
before download (defense in depth, never trusts the wire even from its
own primary). Also independently reproduced that the regression test
fails against the pre-fix code and confirmed the QR-pairing lifecycle
test (`sync_api_test.go`) matches real production behavior — revoking a
till's bearer deletes the row outright (not just delists it), so the old
bearer immediately stops authenticating; one-time-token reuse rejection
is the real `enrolTokens.consume` deletion mechanism, not incidental.

## What's covered in this batch

- `enrolTokens.issue/consume` (one-time, expiry).
- `withinLast` (the sync-chip freshness check).
- The full QR-pairing lifecycle: issue token → enrol → ping → revoke →
  ping fails; one-time token reuse rejected; garbage/expired tokens
  rejected.
- Manager-gating on `/tills`, `/api/sync/enroll-token`.
- `/api/sync/promote`'s confirmation-phrase and actual-replica-state
  guards.
- `/api/sync/snapshot`'s bearer requirement.
- `/api/sync/join`'s garbage-code rejection.
- `listItemAssets`/`safeAssetPath` (the regression above, plus the
  traversal guard).

## Not covered in this batch (deferred)

`joinPrimary` (the full replica-side join — enrol, download snapshot,
stage restore+identity; needs a fake primary via `httptest.Server`),
`GET /api/sync/admin`/`GET /api/sync/stock` (the admin/stock bundle
dump+apply, `sync_admin.go` — needs many more seeded tables for a
realistic `DumpAdmin`), `StartSyncPush`/`StartSyncPull` (the background
polling loops — real `*http.Client` against a configurable primary URL,
same shape as `StartSyncPush` deferred in batch 10, low risk since
ADR-0003 guarantees checkout never depends on them), and the sync-chip
HTML rendering itself (`GET /ui/sync-chip` — `withinLast`, its one real
piece of logic, is covered directly).

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
