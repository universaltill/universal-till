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


## Combined review 2026-11-27 (ut-docs#7097 batch, one Reviewer: not the Dev, not the Tester)

Independent security pass by a different-model subagent (Fable) plus the Reviewer's own reading, against the binding review-adr.md conditions (B1-B6, H1-H2, M1-M9). Pre-flight: walked PREFLIGHT.md for the touched surfaces; the batch's combined Tester report (tester-report.md) is the test evidence; no real shop or person names in the added tests.

Findings and outcome:

1. HIGH, fixed. Rename-keeps-row took an existing quiet till's row on a self-asserted `device_id` and ANY valid enrol token. It now needs a manager approval of a pair request that named that device (`RequesterDeviceForToken`), like the name rule; a typed code or QR token never unlocks it. Tests in `sync_api_test.go` (unapproved enrol adds a row and leaves the quiet one alone).
2. MED, fixed. `tills.device_id` is nulled in the join snapshot (it was only redacted in the admin bundle). Test `TestRedactedJoinSnapshot_NullsTillDeviceIDs`.
3. MED, fixed. The unauthenticated pair-request route deleted every pending row of a caller-supplied device id; it now deletes only rows of the same device id AND name. Residual: a LAN caller can still flood requests (5 a minute limit, as before).
4. LOW, fixed. `SetDeviceID` gives a device id to one row only (the first claimant).
5. Verified: migration 118 collides with nothing in the four PRs; restored till never auto-registers; key-refused hold expires after 24 h.
6. Merge note: this PR and #2762 both change `claim.md`, `multitill.md` and `locales/en.json`; merge #2759 first, then the UI PR meets three small conflicts (resolution in reviewer-report.md).

Verdict: SHIP after the fixes above.
