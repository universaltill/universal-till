# Review: one tree for Tills & devices (ut-docs#7173, #7097) — PR #2790

Reviewer: the one combined Reviewer (Sonnet 5.5 orchestrating; this run did the review inline, no
separate Fable subagent was spawned: Skill(scrum-master)/Skill(reviewer) are not registered in this
session, the reviewer SKILL.md was read from ut-docs). Scope: `git diff origin/main...HEAD`, security
and cross-PR focus.

Pre-flight: guards run and green: guard-i18n, guard-i18n-meta, guard-no-showmodal, guard-no-native-confirm,
guard-rtl-logical-css, guard-help-topics, guard-compliance-claims, guard-competitor-naming,
guard-core-neutral, guard-fixed-dates, guard-wip-commits. The rest of ci.yml `build` guards and the full
`go test ./...` were not run (Tester owns them; Tester is running the server + Playwright).

## What shipped
Settings gets six Tills & devices sections plus Kiosk in one tree; `/tills` and `?tab=` URLs 303 into it;
"Old entries" with a banner and inline manager-PIN removal; one Save on This till.

## Findings
1. **IMPORTANT, fixed: PIN only checked in the browser on the local revoke path.** The inline panel called
   `verify-pin` and then posted `/api/sync/tills/{id}/revoke`, which trusted the session. A direct POST (or
   any script on a manager session) skipped the PIN the owner mandated; every UI removal incl. the roster
   Revoke goes through the panel. Severity: medium (needs a manager session, same authority the old one-tap
   revoke had, but the owner's rule is "no removal without the PIN"). Fix: the revoke handler calls
   `verifyRemovalPIN` (after the cheap replica/409 check so replicas never burn lockout attempts, before any
   delete). `verify-pin` stays only so "Remove all" fails early on a wrong PIN before removing anything.
   `remove-from-account` already checked the PIN server-side. Tests: `TestRevoke_EnforcesTheManagerPinServerSide`
   (no PIN 401, wrong PIN 401, right PIN 204 and row gone); existing revoke tests (sync_api, sync_link,
   sync_link_client, receipt_prefix_5074) now post the manager PIN and the sync test deps carry an AuthSvc and
   a seeded manager.
2. **IMPORTANT for merge order, not fixed here: overlap with PR #2786 (ut-docs#7098).** #2786 already ships
   the real cloud-only removal (`POST /api/sync/tills/cloud-remove`, `enroll.RemoveFromAccount`, behind the
   default-off `tills.cloud_remove_enabled` setting), `revoke-stale`, `authorizeTillRemoval`, `revokeTillAndRetireRegister`
   (the revoke now retires the register) and `tills_tab_others.html` edits. This branch keeps a stub seam
   (`accountRemovalReady=false`, `removeFromAccount`, route `remove-from-account`) that duplicates cloud-remove.
   Decision: do not duplicate; whoever merges second:
   - point `removeFromAccount`/RemoveURL for cloud-only rows at `cloud-remove` (fields `device_id`, `manager_pin`)
     or delete the stub route; keep the setting default off;
   - **#2786's `authorizeTillRemoval` admits any `sync_management` session WITHOUT a PIN**, and its `revoke` /
     `revoke-stale` / `cloud-remove` take no PIN, so the PIN rule is not held on that PR's routes. After merging,
     replace `authorizeTillRemoval`'s session branch with `verifyRemovalPIN` (this branch's helper) on all three
     routes. The line added to `revoke` here will conflict textually with #2786's revoke edit (trivial: keep both,
     PIN check before `revokeTillAndRetireRegister`).
   - `tills_tab_others.html` is deleted here, edited there: resolve as deleted, port #2786's Others-tab changes
     into `tills_section_others.html`/`tills_section_old.html`.
   - `sameDeviceHint` here is a seam; #2786 has `internal/tills/samedevice.go`: wire it, drop the heuristic.
3. Redirects / old URLs: `tillsRedirectTarget` builds the target from a fixed path plus an allow-listed `need`
   and `refresh=1`; no user-controlled host or path, so no open redirect. Legacy `/ui/tills/tab/{id}` kept one release.
   OK.
4. Caches: the Old-entries count is read from the cached account list only, never fetched (test
   `TestOldEntriesCountCached_NeverFetches`); `fleetCache` is reset on removal; no new unbounded map. OK.
5. i18n: en.json + en.meta.json only, guards green. 63 orphan keys reported by guard-i18n; only
   `settings.tills.help` is tills-related, the rest pre-exist. Dev's `unused-keys.txt` cleanup is deferred (nit,
   backlog line on the epic).
6. UI rules: no `showModal`, no `window.confirm` (inline panel is `role=group`, error `role=alert`, label bound to the
   input, Escape returns focus to the opener, Enter confirms). No "master/slave" (`TestTillsTree_NoMasterSlaveWording`).
   RTL guard green. Help: `multitill.md` documents Old entries and the PIN; routes front-matter passes guard-help-topics.
7. Nit, accepted: the squashed first commit subject ends in "WIP" (already pushed; guard-wip-commits passes, it only
   matches the placeholder subject).

## Verification
`go test ./internal/pages/ -run 'Sync|Revoke|Tills|Old|Roster|S0_|Reader|PairingManagerGate|TargetedSwap|TaxHook'` ok.
The new test was not reverted-to-red separately: before the fix the handler had no PIN call (visible in the diff), and
the three existing revoke tests that post no PIN failed with 401 after it, proving the check is live.

## Verdict
Approve after the fix above, conditional on the #2786 merge-order notes in finding 2 (second PR to merge owns the
reconciliation). Deferred: cloud-only removal wiring, same-device unification, orphan key cleanup.
