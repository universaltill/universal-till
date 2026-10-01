# Review: joined till shows "registered through the main till" (ut-docs#2753)

- **Card:** universaltill/ut-docs#2753 (follow-up of #2730), lane:cloud-24
- **Branch:** `fix/2753-replica-registered-via-main`
- **Built by:** Sonnet (complexity:easy). **Reviewed by:** Opus 5.5, fresh context, in a separate worktree.

## What shipped

A joined till (replica) that its main till registered in the cloud
(`POST /api/sync/cloud-device`, `enroll.applyVouch`) has no store token of its
own, so `enroll.CurrentStatus().Registered` was false. That till showed
"Marketplace: not connected" in the status bar and "This till is not
registered yet." with **Register now** in Settings → Till registration.
Pressing Register now succeeded (the vouch) but still printed ❌.

- `internal/enroll`: `Status.ViaMainTill` (true only when `Registered` is
  false and the main till vouched for the till's current device id), backed by
  a mu-guarded `vouchedDevice`. It is set at `Init` from the persisted
  `marketplace.device_registered` marker (replica only, not for env-pinned
  identities, after `repairCopiedIdentity`), and in `applyVouch` while the
  till is still a replica. `ForgetReplicaVouch()` clears it.
  `Registered` keeps its meaning.
- `POST /api/sync/promote` calls `enroll.ForgetReplicaVouch()` after
  `ClearReplicaIdentity` (promotion doesn't restart the process).
- Template func `enrolledviamain`. The status-bar chip is hidden for it. The
  Settings card has a new branch: ✅ "This till is registered through the main
  till.", store id, device id. It has no fleet list, claim button or Register
  now, because all three need the store token only the main till holds.
- `POST /api/enrol/now` treats `ViaMainTill` as success.
- New key `settings.enrol.registered_via_main` in en/tr/ar/fa. Language-pack
  follow-ups for de/es/pt are on branch `i18n/2753-registered-via-main` in each
  pack repo.
- Help `claim.md` step 1 (en/de/tr/ar/fa): what a joined till shows, and that
  claiming is done on the main till.
- Docs-shots surface hash refreshed (`Docs-Shots-Unchanged`): the new branch
  renders only on a vouched replica, and the docs-shots till is a main till.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | tr used "birincil kasa" (= "primary"). The repo's term for main till is "ana kasa" (`settings.error.*main_till*`, `multitill.md`). | Fixed in tr.json and `help/tr/claim.md`. |
| 2 | should-fix (low) | A vouch request already in flight when the till is promoted could reach `applyVouch` after `ForgetReplicaVouch`. The promoted main till would then say "registered through the main till" until it restarts. | Fixed: `applyVouch` sets the state only while `isReplica` (promote clears `sync.primary_url` first). New test `TestLateVouchAfterPromoteIsNotViaMainTill`: red without the guard, green with it. |
| 3 | nit | `Init` read `device_registered` while holding `mu`. | Fixed: the read was moved above the lock. |
| 4 | nit | The state reflects "last vouched", not "currently healthy". It is driven by the persisted marker, as before. | Accepted. This matches the marker's existing semantics. |
| 5 | nit | No test for the `/api/enrol/now` via-main branch or the promote call site. The page test's cleanup re-ran `Init` with a cancelled `t.Context()`. | Cleanup now uses `context.Background()`. The two call sites are accepted untested: each is a one-line wiring onto unit-tested functions. |

The reviewer checked the following and found no issue: lock ordering
(`applyVouch` callers hold only `attemptSem`, never `mu`); token rotation
giving a replica its own token (`Registered` wins); `Init` ordering against
`repairCopiedIdentity`; the replica check matching `isReplica`; `ClaimCode`
needing the token (so the help text is accurate); no file writes or
cwd-relative paths.

## TDD

- The enroll tests were seen failing first (compile error, then assertions).
- The reviewer reverted `CurrentStatus` to `ViaMainTill: false`. These failed:
  `TestViaMainTillTrueAfterVouchOnTokenlessReplica`,
  `TestInitReplicaWithPersistedVouchSetsViaMainTill` and
  `TestSettingsAndStatusBar_ReplicaRegisteredViaMainTill`. All passed again
  after restoring.
- The orchestrator saw the finding-2 test fail with the guard removed, then
  pass with it restored.

## Verified beyond automated tests

A real driven run used a seeded demo DB marked as a vouched replica
(`sync.primary_url`, `marketplace.device_id` = `marketplace.device_registered`,
no token) with an unreachable main till. Screenshots of Settings → Till
registration were taken at 1024×600 (en, ar) and 360×800 (en), and looked at.
Each shows ✅ "registered through the main till", the store and device ids,
and no Register now or claim button. There is no "Marketplace: not connected"
chip, and the RTL layout is correct in ar. Not checked on real touch
hardware; this change adds no new interactive control.

## Gate

`gofmt -l .` (empty), `go build ./...`, `go test ./...`, `go test -race
./internal/enroll/`, `golangci-lint run ./...` (0 issues). Guards: i18n,
help-topics, help-drift, docs-shots, data-access, core-neutral, kiosk-engine,
no-showmodal, compliance-claims, competitor-naming, emoji-font,
page-http-error, htmx/osk-loaded, autofill-suppression. All pass.

## Verdict

Safe to merge.
