# Test coverage batch 27: sync_api.go's joinPrimary + sync_assets.go

2026-07-29

`internal/pages/sync_api.go`'s `joinPrimary` (the replica-side "join a
shop via QR code" flow — ~8.3% covered) and `internal/pages/sync_assets.go`
(primary-side item-image manifest/file serving + replica-side download
logic — `registerSyncAssets` 0%, `syncItemAssets` 16%).

Implemented by an Opus-model agent while this session (Sonnet) worked
batch 28 in parallel — same cross-model-review flow as batch 25.
Reviewed and committed here after independent verification below.

## No bug found — legitimate outcome, like batch 26

Both files behaved as documented once actually exercised. In particular,
`sync_assets.go`'s `assetsRoot()` (the exact function batch 11 fixed for
a cwd-relative-path regression) was re-verified still correct — still
resolves to `paths.Data("public","assets","items")`, still matches the
actual upload path in `catalog/handlers.go`. Re-checking a previously-
fixed bug class rather than assuming "already fixed, skip" is the right
instinct for this kind of push.

## Independent verification (sonnet, different model from the Opus implementer)

- Read both diffs in full (`sync_api_test.go` +256 lines, `sync_assets_test.go`
  +244 lines). No false-pass patterns: assertions check real staged-file
  state (`appdb.PendingRestore`, `appdb.ReplicaIdentityPath` contents),
  real HTTP status codes, real byte-for-byte file contents, and fetch
  counts (to prove the skip-when-unchanged optimization actually skips,
  not just "didn't error").
- `go build ./...`, a full `go clean -testcache && go test ./...` (whole
  repo), and both CI guards — all pass.
- Coverage confirmed matching the implementer's report: `joinPrimary`
  8.3%→86.1%, `registerSyncAssets` 0%→90.9%, `syncItemAssets` 16%→74.0%.

## Coverage added

**`joinPrimary`** (via a real `httptest.Server` primary running the
actual `registerSyncAPI` handlers, not a stub):
- Full happy path: a live one-time enrol token issued by the primary,
  consumed by the replica's `POST /api/sync/join`, snapshot downloaded
  and staged (`restore-pending.db` present via `appdb.PendingRestore`),
  a `replica-identity.json` staged alongside with the correct primary
  URL/bearer/till name/receipt prefix (`T2-` — primary is till 1, first
  replica numbers from 2), and a `joined_primary` audit row.
- Primary unreachable → 502, nothing staged.
- Primary rejects the enrolment token (never issued / already consumed)
  → 502, nothing staged.
- Enrolment succeeds but the snapshot download 500s → 502, and —
  importantly — **neither the restore file nor the identity file gets
  staged**, the offline-first "no half-applied restore" invariant.

**`registerSyncAssets`** (primary side): unauthenticated manifest/file
both 401; an authenticated manifest lists an uploaded file with its real
byte size; an authenticated file fetch returns the exact bytes; a
`../../../etc/passwd`-style traversal path is rejected with 400 even
with a valid bearer (auth alone doesn't grant arbitrary file reads).

**`syncItemAssets`** (replica side): a missing file downloads, then a
second tick with a size-matching local file is skipped (fetch count
asserted at 1, not just "no error"); a size-changed file re-downloads;
a primary manifest 500 is non-fatal and writes nothing (best-effort,
non-blocking — offline-first); a primary file-fetch 404 is skipped, not
retried or errored; a traversal path advertised by a (malicious or
buggy) primary is dropped by `safeAssetPath` before any fetch is even
attempted.

## Verification

`go build ./...`, `go clean -testcache && go test ./...` (whole repo),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.
`internal/pages` coverage: 63.2% (combined with batch 28, committed
just before this one).
