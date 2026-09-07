# ut-docs#1752 — reseal pre-existing unsealed plugin-setting rows on reconcile

Date: 2026-09-07
Branch: `fix/1752-reseal-preexisting-plugin-settings`
Reviewer: independent subagent, fresh context, Sonnet (`complexity:easy` per
the scrum-master skill's model routing — reviewed in a worktree-isolated
instance that never saw the implementation reasoning).

## Report

Follow-up from the independent review of ut-docs#1746
(`docs/code-reviews/2026-09-07-reconcile-plugin-settings-seal-seam-1746.md`).
#1746 made `ReconcilePluginSettings` seal a manifest `default_value` for a
secret-typed/heuristic-matching key when it writes a **new** row. It never
touched the existing-row branch: a plugin installed before #1746 shipped,
whose secret setting was still sitting on its cleartext manifest default
(never explicitly configured by an operator), stayed in cleartext forever
across every future reconcile/upgrade of that plugin — the row only healed if
an operator happened to overwrite that exact key by hand.

## Root cause

`internal/data/plugin_repo.go`'s `ReconcilePluginSettings`, existing-row
branch (the loop over `byKey`): it only ever updated a row's `scope` when the
manifest's declared scope differed from what was stored. It never rewrote
`value_json`, so an unsealed value found on entry stayed unsealed on exit, no
matter how many times reconcile ran.

## Change

- The existing-row branch now also reseals `best.valueJSON` in place when
  `!secrets.IsSealed(best.valueJSON)`, through the same `sealSettingValue`
  seam every other writer in this file goes through (declared-secret OR
  key-name-heuristic match → sealed; anything else → returned unchanged, so
  the call is a safe no-op for ordinary settings).
- Deliberately a **separate UPDATE statement** from the existing scope-move
  branch, so a reseal-only pass (scope unchanged) never touches `scope_id` —
  the scope-move branch's `scope_id = NULL` is only correct when scope
  actually changes; folding reseal into that same statement would have
  silently clobbered a register-scoped row's `scope_id` on every reconcile
  that only needed a reseal.
- A seal failure (no key store available) aborts the whole
  `ReconcilePluginSettings` call, same fail-closed contract as every other
  write in this seam — never falls back to leaving the row in cleartext.
- Non-goal, unchanged: nothing on the read path (`GetPluginSetting`/
  `ListPluginSettings` already open transparently regardless of seal state).

## What the independent review found

Verdict: **SAFE TO MERGE**, no blocking issues.

- **TDD claim re-verified independently.** The reviewer reverted just the
  new reseal block (kept the new test), reran it, and got a real assertion
  failure (`want sealed after a reconcile`), then restored the fix and
  confirmed it passes — not taken on trust.
- **Correctness walked through case-by-case**: heuristic-match key, a
  declared-secret key whose name matches no heuristic (`merchant_code`), an
  ordinary key (must stay untouched, and does — `sealSettingValue` is a
  no-op for it so no UPDATE fires at all), and an *already-sealed* row (the
  `IsSealed` prefix check gates entry into the branch, so no double-seal
  risk).
- **`scope_id` isolation confirmed by reading the actual SQL**, not just the
  test: the reseal `UPDATE` statement's `SET` clause never names `scope_id`,
  so it cannot be reset regardless of test coverage — the test (asserting a
  register-scoped row's `scope_id` survives) corroborates this rather than
  being the only line of defense.
- **Fail-closed path verified live.** The shipped diff had no test for the
  reseal branch's own fail-closed behavior (only the sibling insert-branch
  had one). The reviewer confirmed manually (scratch test, not shipped) that
  a seal failure here does abort the reconcile and leaves the row
  untouched — then this was closed for real before merge: added
  `TestReconcilePluginSettings_ResealExistingRowFailsClosedWithoutKeyStore`,
  mirroring `TestReconcilePluginSettings_DefaultValueFailsClosedWithoutKeyStore`'s
  existing pattern.
- **Duplicate-row interaction checked**: the reseal logic runs on `best`
  only, after every non-best duplicate is already deleted — no risk of
  reseal touching a row about to be removed.
- **Nit, accepted as pre-existing and deferred**: the duplicate-row
  tie-break (`valueJSON != d.ValueJSON`) doesn't account for seal state, so
  a sealed row could in theory out-rank an unsealed cleartext duplicate for
  the same key in the tie-break comparison. Not introduced by this change,
  and not currently reachable — the insert branch never creates a second row
  for a key that already has one, so no live duplicate-of-mixed-seal-state
  case exists today. Recorded here rather than a new card, since it isn't
  reachable from any current code path; worth another look if that ever
  changes.
- No real client/shop name and no literal credential anywhere in the diff —
  test fixtures use obviously-fake placeholders
  (`sk_default_from_manifest`, `M-DEFAULT`).

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean.
- `go test ./internal/data/... ./internal/plugins/... ./internal/plugins/marketplace/... ./internal/plugins/oauth/...` — all pass, including the full `internal/data` suite (not just the new test), both before and after adding the fail-closed test.
- `go test ./...` (full repo) — all pass.
- `bash scripts/ci/guard-data-access.sh` — pass (no SQL added outside `internal/data`).
- `golangci-lint run ./...` — 0 issues.
- Backend/data-layer only — no UI, HTTP route, or locale string touched, so
  no visual check and no manual-topic update apply here.
