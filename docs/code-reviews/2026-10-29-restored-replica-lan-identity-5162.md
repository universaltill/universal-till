# Review: a restored replica drops its cloned LAN identity and joins again (ut-docs#5162)

**Date:** 2026-10-29 · **Branch:** `fix/5162-restored-replica-lan-identity` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev), building from an Opus Architect brief · **Reviewer:** Opus 5.5 (independent, a different model from the author) · **Tester:** Sonnet (driven run)

## What shipped

- **`internal/enroll/restored.go`**
  - When #5157's restore check fires, `clearClonedIdentity` also clears `till_identity.cloud_id`.
  - On a replica it also clears `sync.bearer`, `sync.till_id`, `sync.primary_cert_pin` and `sync.primary_cert_next_pin`. It sets a provisional receipt prefix `R<4 hex of the new install id>-`.
    - The original till keeps minting `T<n>-` numbers, and `sales.receipt_no` is UNIQUE at the main till. With a copied prefix, the replay would collide and the re-join would be refused.
    - `tillNoOf("R…-")` is 0, so the join grants the copy a new `T<n>-`.
    - An `R…-` prefix that is already set is kept, so a reset that repeats never starts a second series.
  - It keeps `sync.primary_url`, so the till stays a joined till and the #5058 re-join path keeps its sales.
  - It keeps the till name and role, and the push cursors. Already-pushed sales are replayed idempotently by sale id.
  - The env-pinned branch clears the LAN identity too.
  - A replica that only holds a bearer still gets the restored flag.
- **`internal/enroll/replica.go`:** the vouch loop waits quietly (`errNotPairedReplica`) while the till is unpaired.
- **New "Join the main till again" state.** It is a chip and a notice, never a modal.
  - `linkRejoin` in `link_status.go`, and the `rejoin` branch of the `main_till_status.html` status-bar pill, which links to `/tills`.
  - A warning state on the rail sync chip.
  - A `tills-needs-rejoin` notice on `/tills`.
  - The cloud-auth "restored" chip is hidden on a replica. For a replica the fix is the re-join, and the cloud follows through the vouch.
  - `mainTillGone()` (unreachable or rejoin) keeps shop-wide settings and the catalogue locked in `init.go` and `settings_shopwide_lock.go`. The base layout's chip script now locks for `rejoin` too (`mainTillLocks`).
- **`completeJoin`:** a restored copy can never upload the original's unconfirmed day closes, so for it the cloud day-close gate is skipped.
  - A Warn is logged and an audit row `rejoin_day_closes_kept_local` is written.
  - The day closes stay in the `pre-restore` DB copy, which is kept for the retention period.
- **i18n and help**
  - 3 new keys in en/ar/fa/tr, plus their `en.meta.json` metadata. The de/es/pt pack PRs are branches `i18n/5162-rejoin-keys`.
  - `multitill.md` has a new section, and `backups.md` one sentence, in en/de/ar/fa/tr.
  - The help-drift baseline counts that heading. The docs-shots topic and surface hashes are refreshed; no screenshot changed.

No ADR is needed. This reuses the ADR-0116 D5 reset, the ADR-0114 pinned re-join and the per-till prefix; nothing new is cross-cutting.

## Findings (Opus review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | `base.html` called `applyMainTillLock(…, state === 'unreachable')`. When the chip loaded `rejoin`, it unlocked the catalogue that the server had rendered locked, and every save was then refused ("no sync bearer"). | **Fixed:** a `mainTillLocks(state)` helper locks for both states. `TestBaseLayout_RejoinStateKeepsMainTillLock` fails with the old script ("base.html has no mainTillLocks(state) helper") and passes after the fix. |
| S2 | should-fix (scope) | `sync.till_register_id` is kept, so until the re-join the copy's sales are booked to the original's register. Clearing it would block checkout when there are 2+ registers. | **Deferred** to ut-docs#5177. |
| N1 | nit | If writing the marker keeps failing, every boot mints a new R prefix. | **Fixed:** an existing `R<4 hex>-` prefix is kept. `TestRestoreCheck_RepeatedResetKeepsProvisionalPrefix` failed before the fix (`"R33c9-", want … Rbeef-`) and passes after it. |
| N2 | nit | The 4-hex prefix allows 65,536 values. Two copies of one original could collide, and the re-join would then be refused loudly. | Accepted: very unlikely, and it is never silent loss. |
| N3 | nit | The catalogue lock banner on a restored copy says "Can't reach the main till right now". | Accepted: the chip and the Tills notice say "join again". |
| N4 | nit | The `rejoin_day_closes_kept_local` audit row lands in the DB that the join snapshot replaces. | Accepted: it lives with the day closes it describes, the same as the existing `joined_primary` row. |
| N5 | nit | The de help says "Einem bestehenden Geschäft beitreten", but the pack label is "Bestehendem Geschäft beitreten". | Accepted: it matches the wording already used on the page. Not changed. |
| T1 | nit (Tester) | The `/tills` notice says "on the Tills page", but the reader is already there. | Accepted: the hint text is shared with the status-bar chip title. |

The reviewer checked these and found them sound:

- A normal restart does nothing: a matching marker returns before any write.
- A just-joined replica is never wiped: its `till_identity.*` rows are stripped from the snapshot.
- Crash ordering is safe: the reset is idempotent and repeats.
- Every code path that uses the bearer handles an empty one, with no hot loop and no nil dereference.
- No path is left that authenticates the copy as the original.
- The R prefix is never parsed as a till number.
- The day-close skip is reachable only after a restore, because the join snapshot clears `restored_at`.

## TDD re-verification

For each row, the fix was broken, the test run, then the code restored and the test run again (pass).

| Fix broken | Test that failed | Who checked |
|---|---|---|
| `linkRejoin` branch disabled | `TestDeriveLinkView_UnpairedReplicaIsRejoin`, `TestTillsPage_RestoredReplicaShowsRejoinNotice`, `TestLockedSettingsRegions_RestoredReplicaNeedingRejoin` | Reviewer |
| `mainTillGone()` back to `!= linkUnreachable` | `TestLockedSettingsRegions_RestoredReplicaNeedingRejoin` | Reviewer |
| Sync chip `rejoin` forced false | `TestSyncChip_ReplicaWithoutBearerSaysRejoin` | Reviewer |
| `clearClonedIdentity` commented out | `TestRestoreCheck_ReplicaClearsLANIdentity` | Reviewer |
| Day-close skip disabled | `TestCompleteJoin_RestoredReplicaSkipsDayCloseGate` (502) | Reviewer |
| JS lock helper reverted | `TestBaseLayout_RejoinStateKeepsMainTillLock` | Orchestrator |
| Prefix-keep disabled | `TestRestoreCheck_RepeatedResetKeepsProvisionalPrefix` | Orchestrator |

## Verified beyond unit tests (Tester, driven run)

- **Restored boot.** A real binary on a fresh data dir, seeded as a replica, then the marker was changed and the till booted.
  - The bearer, till id and pins are empty. `cloud_id` is cleared. `primary_url` is kept. The prefix is `R0971-`. `restored_at` is set.
- **Normal restart.** A second boot left every `sync.*` and `till_identity.*` row byte-identical.
- **Screenshots, read by the Tester.** The sale-screen status bar at en 1280×800 and 800×1280, and fa 1280×800 and 800×1280. `/tills` at en 1280×800 and 800×1280, and fa 1280×800.
  - The amber "Join the main till again" pill is not clipped and RTL mirrors correctly.
  - There is no modal.
  - The rail chip shows its warning badge.
- **Offline sale.** A cash sale completed with the main till unreachable, as receipt `R0971-000000001`.
- **Not looked at:**
  - A real app dark theme: it is driven by `data-theme`, not `prefers-color-scheme`.
  - German screens.
  - Completing the join against a real main till. That path is covered by `TestRejoin_RestoredReplicaWithoutBearerReplaysAndGetsANewTillNo` on two real DBs.
  - An iOS device run.
- **Gates.** `gofmt`, `go build`, `go vet`, `golangci-lint` (0 issues), and the `ci.yml` build-job guards all pass, with two exceptions that come from the sandbox, not from this change:
  - `guard-deadcode-baseline` fails because the deadcode binary was built with go1.26 against a go1.27 module.
  - `guard-shellcheck-version` fails because shellcheck is not installed.
  - CI runs both.

## Deferred

- ut-docs#5172: move the LAN bearer out of the backed-up DB, or rotate it. That is the only real fix for "anyone holding a backup holds a bearer".
- ut-docs#5173: a copied main till still reports the original's `lan_discovery.till_id`. Check the cloud's merge by till id.
- ut-docs#5177: the register identity (S2).

## Verdict

Safe to merge.
