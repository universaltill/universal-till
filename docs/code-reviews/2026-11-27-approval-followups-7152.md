# Review: bound-approval follow-ups from #7140 (ut-docs#7152)

Date: 2026-11-27. Lane `lane:cloud-41`. Built by Sonnet 5.5 (Dev). Reviewed by an Opus 5.5 subagent, a different model from the author. Tested by a separate Tester agent.

Pre-flight: Till user-visible behaviour n/a (both changed routes are API-only, so no help topic or docs-shots); `reference/` updated in ut-docs (`architecture/pos-auth.md`); new en.json key `elevation.error_approval_changed_api` has its `en.meta.json` entry (English only, ADR-0160); ADR n/a; README n/a; follow-ups none. Supersession check: no commit on `main` since the branch point touches the changed files, and the card was open and claimed by this lane only.

## What shipped

- **`POST /api/pos/sale/status`** is now registered with a method pattern, so other methods get 405 on the POS API mux (404 through the app's catch-all `/`, like every other `POST /…` route). The body goes through `http.MaxBytesReader` at 4 KiB. An oversize body gets 413 before any lookup, PIN check or token mint; malformed JSON still gets 400. The demo allow-list key in `demo_mode.go` was renamed to the method pattern.
- **Approval token store** (`elevation_approval.go`): `mint(actorID, binding)` stores the actor. An actor holding `approvalPerActorCap = 8` live tokens loses its own earliest-expiring token. The global 256-slot eviction stays as a backstop. So one session's burst of refused attempts can no longer evict another cashier's pending approval.
- **JSON `approval_changed`** refusals use the new key `elevation.error_approval_changed_api`. The HTML prompt keeps `elevation.error_approval_changed`.
- Tests: `sale_status_bound_7152_test.go` (405, 413 with no token minted), `approval_store_7152_test.go` (A's spam keeps B's token, A's 9th mint evicts A's 1st, 40 actors × 8 stay ≤ 256), and the stale case of `TestWriteElevationRefusalJSON_Shape`. `TestElevationBind7085_StoreBoundedAndSingleUse` was adapted to the new signature.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R-L1 | Low | Dev changed `TestCheckOrElevateApproval_StaleNeverReachesPINLookup` to fetch a fresh token, because 15 stale c1 tries now evict the original token under the per-actor cap. That dropped the proof that a stale or mismatched presentation never burns the live token. | **Fixed:** each case now starts from a fresh store and mints its own live token. After 5 stale tries, still under the cap, the test asserts `valid(tok, b)` and that the live token still elevates with the manager PIN. |
| R-L2 | Low | A session's own stale or missing-token refusals push out its own older live tokens. Example: one login shared by 8 or more devices or integrations. | **Accepted:** this fails closed (`approval_changed` plus a fresh token), and the newest token, the one on screen, always survives. It is the trade-off the card chose. |
| R-L3 | Low | Actor A can still evict B through the global cap. That needs 256 live tokens while A holds fewer than 8, which means 32 or more distinct staff ids each holding 8 prompts within 5 minutes. | **Accepted:** this is the documented backstop. |
| T-G1 | Low | Tester: the empty actor id (auth off / no session user) had no test. | **Fixed:** `TestApprovalStore7152_EmptyActorIsCappedBucket`. |
| R-N1 | Nit | Through the full app mux, a GET reaches the catch-all `/` and gets 404, not 405. | **Accepted:** this matches every other method-pattern route, and nothing changes state. |

## Verification

- Dev saw each new test fail first. The status tests failed with `GET = 403, want 405` and `oversize = 403 … want 413`. The store tests did not compile against the old `mint` signature. The stale JSON case failed on the old wording.
- The reviewer re-verified a TDD claim in a throwaway worktree: with the per-actor eviction disabled, `ActorSpamDoesNotEvictOthers` and `ActorEvictsOwnOldest` fail, and they pass once it is restored.
- Clean: `go vet ./internal/pages/`; the targeted `go test` with and without `-race`; `guard-i18n.sh` and `guard-i18n-meta.sh`. The full `internal/pages` suite passed in 679s with `-timeout 1500s`. Its runtime is the pre-existing #7091, not this change.
- Tester (separate agent, own worktree, `-race`): targeted suite and both i18n guards pass. Each mutation is caught: removing MaxBytesReader, dropping `POST`, disabling or inverting the per-actor eviction, and reverting the JSON key. Full `go test ./...` and e2e are left to CI and the batch gate. There is no visual surface.

Verdict: approve. No follow-up cards.
