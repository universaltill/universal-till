# Review: replica redeems its own cloud credential (ut-docs#2769 slice 2, ADR-0116 D3)

Date: 2026-10-02 · Lane: `lane:cloud-54` · Branch: `feat/2769-replica-redeem`
Built by Opus 5.5 (dev subagent); independent review by Fable (different model).

## What shipped

- **Main till** (`enroll.registerDeviceID`): reads the optional `data.redeem_code`
  from a `devices/register` 200/201 (16 KiB cap; empty or non-JSON body = no
  code). `VouchForReplica` relays it on `Vouch.RedeemCode`
  (`redeem_code,omitempty`) only when well-formed (printable token shape,
  16–128 chars). `Vouch` still has no token field.
- **Replica** (`enroll/redeem.go`, called from `applyVouch` after the vouch
  marker is persisted, outside `mu`): POSTs `{store_id, device_id, code}` to
  `<endpoint>/v1/stores/devices/redeem` with no Authorization header; accepts
  the answer only for its own device id and the vouched store and a
  well-formed token; adopts it through the shared `adoptOwnCredential`
  (now also used by D4 rotation: memory first, then `marketplace.token`,
  retried if the save fails). `marketplace.store_id` is never persisted here
  (shop-wide, owned by the admin sync); an empty in-memory store id is
  filled so the till counts as registered without a restart.
- Skips: env-pinned token (one warning per process), no store/device/endpoint,
  malformed code, store mismatch (synced, env-pinned or in-memory store id),
  no longer a replica (promoted).
- Failures never fail the vouch or block selling. Transient ones (network,
  429, 5xx) re-vouch on the 30 s…30 min backoff for a fresh code; refusals
  (403, 409 `already_redeemed`/`device_revoked`/`device_exists`) keep the
  6-hour interval.
- Comments in `enroll/replica.go` and `pages/sync_cloud_device.go` now state
  the redeem-code relay and ADR-0116 D3's two residuals (LAN eavesdropper
  race until ADR-0114 `wss`; a hostile main till could redeem the code
  itself — both visible to the owner as `issued_by`).

## Findings (Fable review)

No blocker, no major. Invariants checked by the reviewer with file:line:
no credential crosses the LAN, code/token never logged (log capture is
pre-redaction, so the leak tests are real), own device + store only, env pin
wins, `store_id` not persisted, `marketplace.token` per-till (admin bundle +
join snapshot), bounded reads, no header injection, `-race` clean.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A transient redeem failure waited the 6-hour vouch interval while the 10-minute code expired | **Fixed**: `applyVouchRedeem` reports it; `replicaAttempt` uses the backoff. Test `TestReplicaAttemptBacksOffAfterTransientRedeemFailure` |
| 2 | minor | Store-mismatch guard ignored the in-memory store id from an earlier redemption | **Fixed**. Test `TestReplicaSkipsRedeemOnInMemoryStoreMismatch` |
| 3 | minor | No handler-level test that `/api/sync/cloud-device` relays `redeem_code` | **Fixed**: `TestSyncCloudDevice_RelaysRedeemCode` (verified it fails when the relay is removed) |
| 4 | minor | A redeemed token lost before it is saved (crash) can't be recovered on a replica: the cloud mints no new code for a device with a credential | **Accepted** (same exactly-once property as D4 rotation); recovery is the "Pair with a shop" follow-up card |
| 5 | nit | Code length tolerated up to 1024, cloud caps at 128 | **Fixed** (`maxRedeemCode = 128`), test case added |
| 6 | nit | A vouch landing after promotion still redeemed | **Fixed** (`isReplica` check, as #2753). Test `TestPromotedTillDoesNotRedeemLateVouch` |
| 7 | note | D3 and D4 can both adopt on a legacy-copy replica; last writer wins | Accepted: the cloud's `device_exists` / code-invalidation rules keep it consistent |

## Verification

- TDD: tests written first; failures seen (`v.RedeemCode undefined`, then
  `redeem calls = 0, want 1`, …; for the review fixes `replicaAttempt =
  (6h0m0s, true), want (30s, false)`, `redeem calls = 1, want 0`).
- Re-verified by the orchestrator: removing the `redeemOwnCredential` call
  makes 5 redeem tests fail; removing the relay makes the handler test fail.
- `gofmt -l .` clean, `go build ./...`, `go vet ./internal/...`,
  `golangci-lint run ./...` (0 issues), full `go test ./...` green,
  `go test -race ./internal/enroll/` green, `guard-data-access.sh`,
  `guard-i18n.sh` pass.
- `guard-deadcode-baseline.sh` (`internal/logging/file.go`) and
  `guard-shellcheck-version.sh` (no shellcheck in the cloud container) fail
  identically on `main` — not this change.
- **Not done:** a driven run with a real main till + replica + cloud
  (needs two tills and the cloud; backend-only change, no UI surface). The
  end-to-end path is covered in-process by `TestReplicaLoopRedeemsRelayedCode`
  (real `Init`, fake main till, fake cloud).

## Verdict

Safe to merge. No user-facing strings, no help-topic change (no operator
surface; the status chip already reads "registered" once the replica holds
its own credential).

## Deferred (new cards)

- Settings → Cloud "Pair with a shop" (ADR-0116 D5/D6) — also the recovery
  for finding 4.
- 3 × 401 in a row → keep selling offline, hourly retry, status chip, queued
  uploads kept; `en.json` keys + language packs + `web/help/` topic.
