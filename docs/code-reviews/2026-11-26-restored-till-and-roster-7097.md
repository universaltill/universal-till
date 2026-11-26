# Review: restored till never auto-registers, key-refused / two-machines state, roster fixes (T4 + T7 of ut-docs#7097)

Status: STUB — a Reviewer (not the Dev agent) completes this record.

Scope: `internal/enroll` (auto.go, enroll.go, pair.go), `internal/pages` (register_blocked.go, cloud_auth_chip.go, pairing_api.go, pairing_join.go, sync_api.go, tills_tabs.go, tills_revoke_stale.go), `internal/data` (pairing_repo.go, tills_repo.go, sync_admin_repo.go), `internal/tills/reconcile.go`, migration 118, `web/public/{pairing-stream,tills-live}.js`, roster/others templates, help topics.

## Attack list
1. Restored state blocks both auto-register paths; explicit Pair / new shop clears it.
2. Migration 118: `tills.device_id` nullable and redacted from the admin bundle; checksum pinned.
3. Rename-keeps-row: only for a not-live row, new name free; never touches another device's row.
4. Pair request `device_id` validation; replaced rows are only the same device's pending ones.
5. `tills-live.js` swap logic; `utPairingBusy` touch window.

## Findings
(to be filled in by the Reviewer)
