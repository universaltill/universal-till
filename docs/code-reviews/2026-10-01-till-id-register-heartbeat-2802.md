# 2026-10-01 — till_id on the heartbeat and /stores/register (ut-docs#2802)

Lane `lane:cloud-54`. Two PRs: ut-cloud `fix/2802-register-till-id` and
universal-till `fix/2802-till-id-heartbeat-register`. This record is the
same in both repos.

## What shipped
- **universal-till.** `cloudsync.buildSyncRequest` adds `till_id` (trimmed
  `sync.till_id`) to the heartbeat device record. `enroll.register` adds it
  to the `/v1/stores/register` body. In both places the value is read only:
  it is never minted through `discovery.TillID`, because a joined till gets
  its id at join and an earlier minted one would raise a false `till_id`
  conflict in the cloud. The key is left out when the setting is unset.
- **ut-cloud.** `registerStoreRequest` takes an optional `till_id`.
  `register` applies it with `applyTillID` and then `warnIdentityConflict`
  after the device upsert, the same path `/stores/devices/register` and the
  sync handler use (#2752). An empty `till_id` is a no-op, so older tills
  are unaffected.

## Review
Independent review on a different model from the author (Sonnet built it,
Opus 5.5 reviewed it). No blockers.
- **Should-fix (fixed).** The "without till_id" cloud test never
  re-registered, so it passed on the old code. It is now
  `TestStoreRegisterWithoutTillIDKeepsStoredTillID`: a re-register with no
  id keeps the stored id and raises no conflict.
- **Should-fix (fixed).** The conflict branch on the register re-use path
  had no test. Added `TestStoreRegisterFlagsChangedTillID`.
- **Should-fix (fixed by comment, follow-up filed).** Only a joined till
  has `sync.till_id`, and a joined till registers through
  `registerOnReplica`. So the register half sends nothing on today's
  tills, and the change has a live effect only on joined tills'
  heartbeats: a copied identity is now caught on the next heartbeat
  instead of the 6-hourly vouch. The comment in `enroll.register` says
  this. The main till's own id is ut-docs#3307.
- **Nit (fixed).** The heartbeat no-mint assertion checked the wrong key.
  It now checks `lan_discovery.till_id`.
- **Accepted.** `/stores/register` is unauthenticated. Anyone who knows a
  live anonymous-store `device_id` could already take over that device's
  credential (accepted residual, ADR-0116 D3). With this change they could
  also retire rows that share a `till_id` or a silent row with the same
  name. That is weaker than the accepted residual, and a retired till
  comes back on its next heartbeat.

## Verified
- TDD re-checked by the orchestrator in throwaway worktrees. With the
  production change reverted, `TestStoreRegisterRecordsTillID`,
  `...RetiresOtherRowWithSameTillID`, `...WithoutTillIDKeepsStoredTillID`,
  `...FlagsChangedTillID`, `TestRegisterSendsTillID/set` and
  `TestBuildSyncRequestReportsTillID` all fail. With it restored, they pass.
- ut-cloud `scripts/ci/verify.sh` is green. universal-till: `gofmt`,
  `go build ./...`, `go vet`, `go test ./...`, `guard-data-access.sh` and
  `guard-core-neutral.sh` are clean.
- Not driven against a live cloud. This is a wire-shape change and is
  covered by handler and httptest tests. There is no UI or help surface.

**Verdict:** safe to merge. Merge ut-cloud first; the till's extra field is
ignored until it lands.
