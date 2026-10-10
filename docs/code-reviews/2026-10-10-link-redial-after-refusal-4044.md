# Review: redial a refused main-till link once a pull proves the bearer (ut-docs#4044)

Date: 2026-10-10 · Branch: `fix/4044-link-redial-after-refusal` · Lane: `lane:cloud-24`
Author: Opus 5.5 · Reviewer: Fable (independent subagent, different model)

## What shipped

- `internal/fleetlink/client.go`:
  - New `Client.PairingAccepted()`. It clears `revokedFor` and pokes
    `Redial`, and does nothing when no refusal is recorded.
  - New `warnedFor` field. `OnRevoked` fires once per refusal episode.
    The episode ends only when a link is established (`clearWarned` on
    hello). So a main till that answers pulls but keeps refusing the link
    is reported once, not after every pull.
  - `markRevoked` writes its state before it publishes `ModeRevoked`.
- `internal/pages/sync_primary_watch.go`: `primaryContactOK` calls
  `d.LinkClient.PairingAccepted()`. `primaryContactOK` is the success path
  of the pull, and the link's own hello also calls it (a no-op there).
- Tests:
  - `fleetlink`: a transient 403 followed by `PairingAccepted` relinks.
  - `fleetlink`: repeated refusals after `PairingAccepted` warn once, and a
    refusal after a real link warns again.
  - `fleetlink`: `PairingAccepted` on a healthy link changes nothing.
  - `pages` end to end: the real main-till sync API sits behind a front
    that answers 403 on `/api/sync/link`. Once the front stops refusing,
    the replica's next pull relinks it.

## Root cause

When the link got 401/403/4003, `revokedFor` was pinned to the target, and
only a URL or bearer change or a process restart cleared it. The HTTP pull
uses the same bearer and kept succeeding. Since #2862, that pull also
resolves the pairing-revoked problem, so after a transient refusal the link
stayed off with nothing telling the operator.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | `markRevoked` set `ModeRevoked` before writing `revokedFor`. A `PairingAccepted` that ran in between saw nothing to clear, and the redial was lost until the next pull. The episode test also had a flake window there. | **Fixed.** State is written before the mode. The test now waits on `isRevoked(target)`, not on the mode. |
| 2 | Nit | A stale `revokedFor` from an old pairing makes `PairingAccepted` poke `Redial` on a healthy link. `runLink` treats that as a no-op re-read of the target. | Accepted (harmless). Doc reworded to "a no-op unless a refusal is recorded". |
| 3 | Info | #2862 interaction: in a persistent "pull OK, link refused" state, the keyed `sync.link_pairing_revoked` problem is logged once, resolved by the next pull, and not raised again. The chip still shows Revoked. The reviewer suggests #2862 resolve that key from the link's hello rather than from `primaryContactOK`. | Out of scope here. Noted on ut-docs#4044 for #2862's lane. |

A till that really is revoked gets a 401 on the pull too (`sync_admin.go`,
"sync pull rejected"). It never reaches `primaryContactOK`, so it is never
re-dialled. Both the reviewer and the author checked this.

## Verified

- TDD: the new tests fail before the fix. Three mutants were each killed:
  `PairingAccepted` as a no-op, the old `first` check on `revokedFor`, and
  no `clearWarned` on hello. The `pages` end-to-end test failed on the
  unwired build ("a pull with the same bearer succeeded, but the link
  stayed off") and passes once wired.
- `go test -race -count=10 -run TestClient_ ./internal/fleetlink/` passes.
- `go test -race -count=3 -run 'TestReplicaLink_|TestSyncPull|TestRefreshLink' ./internal/pages/`
  passes. The reviewer re-ran the race runs independently.
- `gofmt` and `go vet` are clean. `golangci-lint run ./...` reports 0 issues.
  `go test ./...` passes.
- These guards pass: data-access, card-data-schema, core-neutral,
  kiosk-engine, netaccess, deadcode-baseline-growth, page-http-error,
  docs-shots and i18n.

## Verdict

Safe to merge. Backend only, with no UI or locale change. No reference doc
describes the link's refusal behaviour, so no help or docs update is needed.
