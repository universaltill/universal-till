# 2026-09-22 — SessionBasketManager.HasItems lock-scope fix (ut-docs#2449)

## What shipped

Follow-up from independent review of ut-docs#2444
(`docs/code-reviews/2026-09-21-self-order-empty-session-short-busy-window-2444.md`),
filed as its own card per that review's "Deferred / not this card" note —
same lock-scope bug class ut-docs#2443 closed on `BindTable`'s bind path
and ut-docs#2444 closed on `BindTable`'s busy-check path.

`internal/pos/session_manager.go`'s `SessionBasketManager.HasItems()`
iterates every live session under the manager's own `m.mu` and used to
call `sb.svc.Basket().ItemCount() > 0`. `Service.Basket()` unconditionally
calls `recomputeTotals()`, which — for any session with a tax/charge-policy
asker installed (any country plugin, ADR-0061) — can perform a blocking
plugin round-trip while `m.mu` is held, serializing every OTHER live
session's request behind it. `HasItems` is used by the unattended-update
scheduler to decide whether it's safe to restart the till under a customer
mid-order — not a per-request hot path, so the blast radius is smaller
than the two precedent cards, but the same exposure class.

- `internal/pos/session_manager.go`: `HasItems`'s predicate changed from
  `sb.svc.Basket().ItemCount() > 0` to `len(sb.svc.Lines()) > 0` —
  `Service.Lines()` is a lock/copy/unlock with no recompute and no plugin
  call, mirroring #2444's own fix.
- Doc comment update: the file's top-of-file locking-convention block
  previously listed `HasItems` alongside `SetConfig` as still carrying the
  blocking-ask exposure; corrected to say `HasItems` no longer has it
  (`SetConfig` still does — unchanged, still out of scope, still not filed
  as its own follow-up).
- Test: `TestSessionBasketManager_HasItems_DoesNotBlockOnChargePolicyAsk`
  in `internal/pos/session_manager_test.go`, modeled on the existing
  `TestSessionBasketManager_BindTable_SlowChargePolicyAskDoesNotBlockOtherTables`
  pattern (reuses the `slowChargeAsker` test helper already in that file).
  Installs a stuck charge-policy asker on a session, calls `HasItems()` in
  a goroutine, asserts it returns within 2s — i.e. never invokes the
  blocking ask.

## TDD

Confirmed red pre-fix (test times out at 2.00s reporting "HasItems
blocked on a plugin charge-policy ask"), green post-fix
(sub-millisecond return).

## Independent review

Sonnet, fresh context, isolated worktree (`isolation: "worktree"`), no
visibility into the Dev reasoning — the card is `complexity:easy` so
review runs at the same model tier, in a clean instance, per
`scrum-master`'s model-routing rule. Ran `go build ./...`, `go vet
./internal/pos/...`, `gofmt -l` on both changed files, `golangci-lint run
./internal/pos/...` (0 issues), the targeted `HasItems`/`SessionBasketManager`
tests under `-race`, the full `internal/pos` package under `-race`
(127.3s, all green), and both `guard-data-access.sh`/`guard-kiosk-engine.sh`.

**No findings.** Specifically checked and confirmed clean:
- `len(Lines()) > 0 ⟺ ItemCount() > 0` always holds for this call site —
  zero-quantity lines are removed synchronously elsewhere in `Service`
  (`removeLocked`/`removeLineLocked`), so `s.lines` can never hold one;
  `ItemCount()` floors weighed lines at 1 and rounds unit quantities, so
  any qty > 0 contributes ≥ 1. Same conclusion the #2443/#2444 precedents
  reached for their own call sites.
- Brings this method in line with `Service.HasItems()` (a different type,
  `internal/pos/hold.go`), which already uses `len(s.lines) > 0` — this
  fix removes an inconsistency rather than introducing a new convention.
- No nil-safety change: `sb.svc` is populated by `m.factory()` at session
  creation and never nil for a live map entry, same as before.
- Grepped every `HasItems()` call site across `internal/pos` and
  `internal/pages` — none depend on summed-quantity semantics.
- Doc comment accurate post-edit, no new staleness.
- Test genuinely proves the fix, not scheduling luck — the reviewer's own
  revert reproduced the exact predicted failure at 2.00s; the fixed
  version returns sub-millisecond because it never touches the asker.

**Independently re-verified the TDD claim**, in the isolated worktree:
reverted the one-line fix, re-ran the regression test, confirmed it
failed with the exact predicted message and timing; restored the fix,
confirmed the full package green again; confirmed the working tree
matched the original diff exactly before finishing.

## Confirmed clean, no action needed

- Backend-only diff (`git diff --stat`: 2 `.go` files, no `web/ui/*.html`,
  no new copy/i18n key, no new modal/screen) — UX-guidelines checklist and
  help-manual check don't apply.
- No inline SQL outside `internal/data`/`internal/db`
  (`guard-data-access.sh` green); no self-order handler references the
  cashier's `Engine` (`guard-kiosk-engine.sh` green).
- No secret-shaped literal; test data is generic (no real client/shop
  name).
- No new data race: `Lines()` returns a fresh copy under `s.mu`, safe to
  read after the manager's own lock is released.

## Deferred / not this card

`SetConfig` still has the same blocking-ask exposure `BindTable`/`HasItems`
used to (its `Service.SetConfig` call can trigger a blocking plugin ask via
`recomputeTotals`, under `m.mu`) — untouched by ut-docs#2443/#2444/#2449,
all scoped to their own call paths; not yet filed as its own follow-up.
Worth a human's call on priority, same as this card originally was.

## Verdict

Safe to merge. PR references `Closes universaltill/ut-docs#2449`.
