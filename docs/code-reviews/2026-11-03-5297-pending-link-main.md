# Review: main-till side of the joining till's pending link (ut-docs#5297, slice B1)

Branch `feat/5297-pending-link-main`.
- **Design:** the Opus Architect, under the ADR-0114 amendment 2026-11-03 (ut-docs#5336, merged first).
- **Build:** a Sonnet Dev agent.
- **Testing:** a separate Opus Tester agent, with mutations and a driven pinned-TLS harness.
- **Review:** an independent **Fable** security reviewer (high-risk: an unauthenticated endpoint).
- **Fixes:** a fresh Sonnet agent. The orchestrator re-verified the red/green of the fixes.

The joining-till side is ut-docs#5330.

## What shipped

- `internal/fleetlink/pending.go` adds `PendingHub`. It is separate from `Hub`/`Peer`, because `Peer` always sends `hello` (till id, cursors, pin) and answers unknown frames.
  - One link per request id: a new link replaces the old one with close 4001.
  - At most 8 links in all; over that, `503` + `Retry-After`.
  - 4 KiB reads.
  - Inbound frames are limited to `ping`, `pong` and `bye`; anything else closes with 1002.
  - The link sends one `pairing{approved|denied|expired}` frame and then closes. It closes on approve, deny, its own expiry timer, or shutdown (`bye`).
- `internal/pages/sync_link.go` adds a no-bearer branch on `GET /api/sync/link`, picked by the `X-UT-Pair-Request-Id` / `X-UT-Pair-Request-Secret` headers before `syncTill`. A bearer together with those headers is refused.
  - Check order: TLS and no query string, then a dedicated 5/min per-source limiter (`429` + `Retry-After: 60`), then replica/replaced/nil-hub, then credentials.
  - `commitmentMatches` runs against a dummy commitment for an unknown id.
  - Every refusal returns the bearer path's own 401, byte for byte.
  - Nothing logs the URL, the headers or the secret.
- `internal/pages/pairing_api.go`:
  - The pair-request answer carries `pending_link: true`.
  - The per-id GET reads the secret from the header and still accepts the legacy query form.
  - Approve and deny call `Resolve`.
- `Deps.PendingLinks` is wired in `init.go` and closed on shutdown.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (test quality) | The pages-level `recheck` closure (5 branches) had no coverage, and one test's name claimed the race case it did not assert. | **Fixed:** a scripted `GetByID` seam; `TestPendingLink_RecheckAtRegistration` covers approved, denied, and a DB error that keeps the link open. The test is renamed `..._ApprovedBeforeOpenIsRefused`. The recheck's `expired` branch is still uncovered (a timer race in tests); it is covered at the hub level by #2's test. |
| 2 | minor | The expiry timer sent `expired` blind, but an approve in the last second (second-precision `expires_at`) still succeeds, so B2 would show "declined/expired" for an approved request. | **Fixed:** on expiry the link re-reads the request first. `TestPending_ExpiryRechecksBeforeSayingExpired`: red without the fix (got "expired", want "approved"), green with it, both re-run by the orchestrator. The amendment is updated. |
| 3 | minor | Replica/replaced settings reads ran before the TLS check and the limiter, so they were unlimited, the state was disclosed before credentials, and the comment and T13 overstated the order. | **Fixed:** the order is now TLS/no-query → limiter → replica/replaced → credentials. T13 renamed: a rate-limited request on a replica gets 429, not 409, and reads no pairing row (red first with 409). |
| 4 | minor | The 8-link cap is global, so one LAN host can hold every slot. | **Accepted:** joiners then fall back to today's 15 s poll, never a block (offline-first holds), and the bogus requests show on the Tills page. |
| 5 | minor, pre-existing | The till's `http.Server` has no `ReadHeaderTimeout`/`IdleTimeout`. | Out of scope → ut-docs#5337 (since fixed on `main` by universal-till#2315). |
| 6 | nit | A panic in `recheck` leaked the id's slot. | **Fixed:** unregister is deferred. `TestPending_PanicInRecheckFreesTheSlot`: red without the fix, re-run by the orchestrator. |
| 7 | nit | The 500 was logged under tag `pairing_api`. | **Fixed:** `sync_link`. |

## Verification

- **Tester:** 8 mutations in a throwaway copy each turned a test red:
  - an unknown id gets its own 401 body;
  - a wrong secret gets 403;
  - the query check is removed;
  - the plain-HTTP check is removed;
  - the limiter moves after the DB read;
  - the pending-status check is removed;
  - `Resolve` fans out to every link;
  - deny no longer resolves.
- **Driven run** over a real pinned-TLS httptest server with the real auth middleware and a migrated DB:
  - a `hello` frame from the peer gets close 1002;
  - a cross-origin upgrade gets 403 and opens no link;
  - a second link for the same id closes the first (4001), and only the new link gets `approved`;
  - a new link after approval gets the common 401.
- **Commands:**
  - `gofmt`, `go build ./...`, `go vet` and `golangci-lint run ./...` (0 issues);
  - `go test -race ./internal/fleetlink/...`;
  - `go test -race -run 'PendingLink|SyncLink|PairRequest' ./internal/pages/`;
  - `go test ./internal/auth/...`;
  - the data-access, netaccess, kiosk-engine and core-neutral guards.
  - The full `internal/pages` suite under -race runs in CI.

- **CI found 2 more problems, both fixed:**
  - `TestNoUnrecoveredGoroutines` (ut-docs#3304): the pending writer goroutine lacked `logging.RecoverAndLog`. It now has it, and the test passes locally.
  - The docs-shots surface hash was stale. It was refreshed with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`), since only JSON/WebSocket handlers changed and no rendered page did.

Pre-flight:
- `web/help/` n/a: nothing visible changes in B1; the help sentence ships with ut-docs#5330.
- `reference/`: the ADR-0114 amendment and `architecture/lan-sync.md` are in ut-docs#5336.
- README n/a; en.json keys n/a (none added).
- Review record: this file.
- Follow-ups: ut-docs#5330 (joining-till side), ut-docs#5337 (server timeouts, fixed by #2315).

Verdict: approve.
