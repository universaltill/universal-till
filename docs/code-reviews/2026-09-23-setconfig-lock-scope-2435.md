# 2026-09-23 — SessionBasketManager.SetConfig lock-scope fix (ut-docs#2435)

## What shipped

Follow-up from independent review of ut-docs#2261 (ADR-0103, per-table
self-order session baskets), filed as its own card per that review's
finding N5 — same lock-scope bug class ut-docs#2443 closed on `BindTable`'s
bind path and ut-docs#2449 closed on `HasItems`.

`internal/pos/session_manager.go`'s `SessionBasketManager.SetConfig`
iterated every live session under the manager's own `m.mu` and called
`sb.svc.SetConfig(cfg)` on each. `Service.SetConfig` sets the new config
then unconditionally calls `recomputeTotals()`, which — for any session
with a tax/charge-policy asker installed (any country plugin, ADR-0061) —
can invoke a blocking plugin round-trip. Held under `m.mu`, a slow/hung
plugin ask on any ONE session's `SetConfig` call serialized every OTHER
live session's own manager operation (`Get`, `BindTable`, etc. — all of
which need `m.mu`) behind it. `SetConfig` runs on every settings save, so
this was the highest-traffic of the three call sites the original review
finding named.

- `internal/pos/session_manager.go`: `SetConfig` now snapshots the live
  `*Service` slice under `m.mu`, releases the lock, then calls each
  snapshotted service's `SetConfig` outside it — the same snapshot-then-call
  shape `BindTable` already uses for exactly this reason.
- Doc comment update: the type's top-of-file locking-convention block
  previously named `SetConfig` as the one method still carrying the
  blocking-ask exposure (`HasItems`/`TableOwner` already cleared per
  #2449); corrected to say all three are now clear, and to note
  `TableOwner`/`TableOwnerActive` were never affected in the first
  place (`TableID()` is a cheap lock/read, no recompute).
- Test: `TestSessionBasketManager_SetConfig_DoesNotBlockOtherSessions` in
  `internal/pos/session_manager_test.go`, modeled on the existing
  `TestSessionBasketManager_HasItems_DoesNotBlockOnChargePolicyAsk` /
  `TestSessionBasketManager_BindTable_SlowChargePolicyAskDoesNotBlockOtherTables`
  pattern (reuses the `slowChargeAsker` test helper already in that file).
  Installs a stuck charge-policy asker on one session, calls `SetConfig` in
  a goroutine, asserts a concurrent `Get` on a different, unrelated session
  completes within 2s, then releases the ask and confirms `SetConfig`
  still reaches every session.

## Requirements verified (BA pass, this card)

`HasItems` and `TableOwner`/`TableOwnerActive` — named in the card's title
alongside `SetConfig` — were checked against the current code before
starting: `HasItems` calls `Lines()` (fixed in #2449, no recompute, no
plugin call) and `TableOwner`/`TableOwnerActive` call `TableID()` (a
trivial lock/read, never had this exposure). Neither needed a code change;
scope narrowed to `SetConfig` alone, which the file's own doc comment had
already flagged as the one still-open case.

## TDD

Confirmed red pre-fix (test fails at 2.00s: "Get is still blocked behind
SetConfig's in-flight charge-policy ask — m.mu is being held across it"),
green post-fix. Verified twice — once by Dev, once independently by the
Reviewer in an isolated worktree (see below).

## Independent review

Sonnet, fresh context, isolated worktree (`isolation: "worktree"`), no
visibility into the Dev's reasoning beyond the card and the diff — the
card is `complexity:easy` so review runs at the same model tier, in a
clean instance, per `scrum-master`'s model-routing rule.

Ran `go build ./...`, `gofmt -l` on both changed files, `golangci-lint run
./internal/pos/...` (0 issues), the full `internal/pos` package under
`-race` (154.3s, all green), and both `guard-data-access.sh` /
`guard-kiosk-engine.sh` (both green). Independently re-verified the TDD
claim: reverted only `session_manager.go`, reran the new test, reproduced
the exact predicted failure at 2.00s; restored the fix, confirmed the
package green again and the working tree matched the original diff
exactly.

Independently confirmed, by reading `service.go` directly:
`Service.SetConfig` really does call `recomputeTotals`, which really can
invoke a blocking plugin ask (unlocking/relocking `s.mu` around the
asker call); `Lines()` and `TableID()` really are cheap, recompute-free
reads, confirming `HasItems`/`TableOwner` needed no change.

**One non-blocking finding, not fixed this card:** releasing `m.mu` around
the batch means two genuinely *concurrent* `SetConfig` calls (e.g. an
admin double-submitting the settings form, or a LAN-sync config push
racing a manual settings save) are no longer strictly serialized against
each other the way the old whole-loop-under-one-lock version was — their
per-session writes can now interleave, so different live sessions could
transiently end up on different final configs until the next successful
settings save overwrites both. Not a data race (`-race` clean; each
`Service.SetConfig` is still atomic under its own lock), narrow (requires
two genuinely concurrent settings pushes — nothing today debounces or
locks that at the HTTP handler level), and self-healing on the next save.
Filed as ut-docs#2461 for a human call on priority rather than fixed
silently or dropped.

## Confirmed clean, no action needed

- Backend-only diff (`git diff --stat`: 2 `.go` files, no `web/ui/*.html`,
  no new copy/i18n key, no new modal/screen, no SQL, no money) — UX
  checklist, help-manual check, i18n/data-access/kiosk-engine guards
  don't apply beyond the two guards run above (which are green).
- No secret-shaped literal; test data is generic (no real client/shop
  name).
- No new data race under `-race` (targeted test and full package).

## Deferred / not this card

ut-docs#2461 (see above) — concurrent-`SetConfig` interleaving, self-healing,
not urgent.

## Verdict

Safe to merge. PR references `Closes universaltill/ut-docs#2435`.
