# 2026-09-18 — wire marketplace.Client.AckDownload into both download paths (ut-docs#2381)

## What shipped

`marketplace.Client.AckDownload` (`internal/plugins/marketplace/client.go`)
existed with tests but had zero production callers — found while doing an
unrelated dead-code burndown (ut-docs#1566). Both till-side download paths —
`MarketplaceInstaller.Install` (`internal/plugins/installer_marketplace.go`)
and `MarketplaceInstaller.DownloadToStore` (`internal/plugins/
installer_store.go`) — issue a download token via `IssueDownloadToken`, then
call `DownloadManager.Download`, but never reported the outcome back to
ut-cloud's `POST /v1/download/ack` (`downloadsvc.Service.AckDownload`, which
consumes the single-use token and records completion/checksum-mismatch
metrics). Not a functional bug for the shop — installs work fine either
way — but the token just expired unused and cloud-side per-download
accounting never populated.

Added a best-effort `ackDownload` helper on `*MarketplaceInstaller`, called
from both `Install` and `DownloadToStore` immediately after their
`DownloadManager.Download` call returns (success or failure), before either
function's own error-handling branch — so it fires for both outcomes but
never for an earlier failure (`IssueDownloadToken` error, incomplete token
metadata, `ResolveURL` error), where no download was actually attempted.
Updated the stale "unwired" doc comments on both the till-side `Client.
AckDownload` and ut-cloud's `downloadsvc.Service.AckDownload` (comment-only,
`ut-cloud/internal/api/downloadsvc/service.go`).

## Independent review — round 1

Opus subagent (isolated worktree, `complexity:medium` routing), fresh
context. Verdict: safe to merge, with two should-fix findings (not
correctness bugs — latency and a data-leak hygiene issue):

1. **Latency**: `ackDownload` was synchronous with a 15s timeout on a fresh
   context (mirroring `Install`'s existing terminal-report defer). Measured:
   a blackholed ack endpoint added up to 15s to `Install`'s own return, and
   `DownloadToStore` — called from an HTTP handler with `r.Context()`,
   which `ackDownload`'s `context.Background()` deliberately ignores — had
   *no* such window before and gained a full 15s one.
2. **URL/signature leak**: `FailureReason` was set to `downloadErr.Error()`
   verbatim. `DownloadManager` wraps the underlying HTTP client error,
   which stringifies the full request URL — the marketplace-issued
   pre-signed bundle URL, including its signature/SAS query parameters —
   so a download failure would send that URL, in the clear, in the ack
   request body.

Plus verified: `tokenResp` non-nil guarantees, the wire field mapping
(`Success` → server-side `ChecksumMatch`), no missed call sites (exactly 2
production `Download`/`IssueDownloadToken` call sites, both wired), no
token leak in logs, red→green revert-restore of the TDD claim (3 of 4 new
tests correctly failed with the feature disabled; the 4th — a negative
test — was independently proven non-vacuous by moving the ack call earlier
and confirming it then correctly fails).

## Fixes applied after round 1

- **Async**: both call sites now use `go i.ackDownload(...)` — fire-and-
  forget. A goroutine can never block its caller, by Go language
  guarantee, so this fully resolves finding 1 for both functions at once
  (no per-function tuning needed). `DownloadToStore`'s HTTP handler is no
  longer held open by a hung ack endpoint at all.
- **Coarse `FailureReason`**: new `ackFailureReason(err)` maps a download
  error to one of a small fixed set of safe strings
  (`checksum_mismatch`/`download_canceled`/`download_timeout`/
  `download_failed`) via `errors.Is` against `errChecksumMismatch`,
  `context.Canceled`, `context.DeadlineExceeded` — never the raw error
  text, so no URL (signed or otherwise) can reach the wire.
- **Nit (cheap, taken)**: both pre-Download completeness checks now also
  require `tokenResp.Token` non-empty — previously validated `BundleURL`/
  `ChecksumSHA256`/`Signature` but not `Token`, so an empty-token response
  would download fine then guarantee a server-side "invalid token" on ack.
- **Nit (cheap, taken)**: `TestStoreDownloadThenInstallLifecycle` now also
  asserts `DownloadToStore` produces exactly one ack and `InstallFromStore`
  (which never calls `Download`) adds none — previously true by
  inspection, not pinned by a test.
- Tests reworked for the async call: a new `ackCapture` type (mutex-guarded
  slice + `waitForCount(t, n, timeout)` poll-with-deadline helper) replaces
  the plain slice the synchronous version could read immediately after
  `Install`/`DownloadToStore` returned. The one negative test
  (`TestMarketplaceInstallerDoesNotAckBeforeDownloadAttempted`) needs no
  wait — that code path never reaches the `go i.ackDownload(...)` call site
  at all, so no goroutine is ever spawned, and asserting zero acks
  immediately stays deterministic.

**Deferred, not fixed** (per round-1 review's own recommendation — real,
but pre-existing on the ut-cloud side and out of this card's scope):
ut-cloud's `AckDownload` returns HTTP 500 on a checksum-mismatch ack even
though the ack succeeds server-side (token consumed, metric recorded),
which makes the till log a spurious warning on every failed-download ack;
and `cloud.proto`'s `AckDownloadRequest` wire message has no
`bytes_received`/`duration` fields, so ut-cloud's own
`RecordDownloadCompletion` always records zero for both regardless of what
the till sends. Filed as a follow-up Backlog card
(universaltill/ut-docs#2388) rather than folded into this PR — the second
item in particular needs a `cloud.proto` change and the same
`manifest-contract-guard`-style cross-repo landing-order care this repo's
signing contract already documents, not something to do inline here.

## Independent re-verification of the fixes (self, not a fresh model pass)

Given the fixes were small, targeted, and directly responsive to a Opus
review's own findings (not new design surface), re-verified personally
rather than spawning a second review round — consistent with
`MODEL-ROUTING.md`'s "a second round has to be earned... scoped to the
fix, not a re-review of the whole diff" for a card that already got one
full independent pass:
- Re-read both fixes against the review's own findings line by line.
- Confirmed `go i.ackDownload(...)` is present at both call sites (the
  only change needed to fully close finding 1 for both functions).
- Confirmed `ackFailureReason` is reached via `errors.Is` against the
  actual `errChecksumMismatch` sentinel `DownloadManager.Download` already
  defines (not a substring match), and that the coarse strings can never
  contain the download URL.
- Attempted a scratch reproduction (blackholed ack endpoint, measuring
  `Install`'s own return latency) as an extra empirical check beyond the
  unit tests; the scratch test itself deadlocked on its own `defer`
  ordering bug (`server.Close()` — registered after `defer
  close(blackhole)` but running first, LIFO — blocking on the still-in-
  flight blackholed handler goroutine that only the later-running
  `close(blackhole)` could unblock). That is a bug in the throwaway
  harness, not the production code: `go f()` cannot block its caller by
  Go's own language semantics, independent of anything the goroutine body
  does. Killed the stuck process rather than fight the harness further;
  relying on that language guarantee plus the full existing test suite
  (which does exercise the async path end-to-end via `ackCapture.
  waitForCount`, just without an artificially-hung endpoint).

## Verified beyond automated tests

- `gofmt -l`, `go build ./...`, `go vet ./internal/plugins/...`,
  `golangci-lint run ./internal/plugins/...` — clean, after both rounds of
  changes.
- `go test ./internal/plugins/...` (full package, all 4 subpackages) —
  green.
- `go test ./internal/plugins/... -race -run 'Ack|Store' -count=3` — no
  races, no flakes across 3 repeated runs (the async goroutine + mutex-
  guarded test recorder is exactly the kind of change worth race-testing
  repeatedly, not once).
- `scripts/ci/guard-data-access.sh` — clean (no SQL outside
  `internal/data`/`internal/db`).
- No real client/shop name, no credential-shaped literal (test config
  values like `"secret-1"` are copied verbatim from pre-existing test
  fixtures already on `main`, not newly introduced).
- Backend-only change, no UI surface, no i18n/money/offline-first surface —
  those checklists don't apply.

## Safe-to-merge verdict

Yes. Both should-fix findings from the independent review are resolved;
the cheap nits were folded in; the one genuinely pre-existing, cross-repo,
out-of-scope gap is tracked as a follow-up card rather than silently
dropped.

## Explicitly deferred

- universaltill/ut-docs#2388 — ut-cloud-side download-ack accounting gaps
  (500-on-failed-ack semantics; `bytes_received`/`duration` not carried by
  `cloud.proto`), both pre-existing and out of this card's scope.
