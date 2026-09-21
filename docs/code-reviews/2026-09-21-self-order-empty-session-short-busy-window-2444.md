# 2026-09-21 — Self-order: empty session gets a short busy-guard window (ut-docs#2444)

## What shipped

Follow-up from independent review of ut-docs#2434 (finding S3,
`docs/code-reviews/2026-09-19-self-order-table-bind-race-2434.md`). The
table-QR busy guard's recency window
(`SessionBasketManager.BindTable`/`selfOrderTableBusyMaxIdle`, 10 minutes)
applied identically whether the session already bound to a table was
empty or not — closing ut-docs#2434's own double-bind race required that
for a non-empty session, but for an EMPTY one it meant a guest could
self-lock their own abandoned-and-forgotten table for up to 10 minutes
with no staff-facing override. Real trigger: a guest scans from an
in-app-browser (Instagram/WhatsApp WebView, its own cookie jar), taps
"open in Chrome" before adding anything, and re-scans with no cookie —
Chrome sees the table as held by a stranger (their own empty session) and
shows the busy screen.

- `internal/pos/session_manager.go`: `BindTable`'s single `maxIdle`
  parameter split into `maxIdle` (unchanged, 10 min in production) and a
  new `emptyMaxIdle`. The busy-scan loop now picks `emptyMaxIdle` as the
  cutoff for a candidate session with zero lines, `maxIdle` otherwise —
  no change to the "no item-count exclusion" atomicity guarantee
  ut-docs#2434 established, only to *which* window applies.
- `internal/pages/self_order_page.go`: new constant
  `selfOrderTableBusyMaxIdleEmpty = 90 * time.Second`, passed alongside
  the existing `selfOrderTableBusyMaxIdle` into the one production
  `BindTable` call site.
- Doc comments on `BindTable`, `TableOwnerActive`, and
  `bindSelfOrderTableSession` updated to describe the two-window
  behavior.
- Tests: every existing `BindTable(...)` call site updated to the new
  signature. The old single-window empty-session staleness test replaced
  with two —
  `TestSessionBasketManager_BindTable_EmptySessionFreesTableAfterEmptyMaxIdle`
  (empty frees after the short window) and
  `TestSessionBasketManager_BindTable_NonEmptySessionOutlastsEmptyMaxIdle`
  (non-empty does NOT free early — proves the split is keyed on item
  count, not a blanket shortening). Two new page-level integration tests
  drive the real HTTP handler with an injected `now`, mirroring the
  existing `TestSelfOrder_StaleSessionNoLongerBlocksBusyGuard` pattern:
  `TestSelfOrder_StaleEmptySessionNoLongerBlocksBusyGuardAfterShortWindow`
  and `TestSelfOrder_NonEmptySessionStillBlocksPastEmptyMaxIdleWindow`.

## Independent review

Opus, fresh context, isolated worktree (`isolation: "worktree"`), no
visibility into the Dev reasoning. Read the diff cold, then actually ran
things: `go build ./...`, `go vet ./internal/pos/... ./internal/pages/...`,
`gofmt -l` on the 4 changed files, `golangci-lint run` (0 issues),
`bash scripts/ci/guard-data-access.sh` / `guard-kiosk-engine.sh` (both
green), `go test ./internal/pos/... -race -run 'BindTable|SessionBasketManager' -v`
(15/15 pass) and `go test ./internal/pages/... -race -run 'SelfOrder' -v`
(61/61 pass, no race warnings). Independently re-verified the TDD claim
by hand-mutating `BindTable` (making the empty branch reuse `maxIdle`
instead of `emptyMaxIdle`) in a disposable copy, confirmed both the unit
and page-level empty-session tests failed with exactly the predicted
symptom, then restored and confirmed green again — done atomically, no
turn boundary between mutate and restore, verified via `git status`
after.

**One should-fix, treated as a blocker and fixed:** the busy-scan
predicate was `sb.svc.Basket().ItemCount() == 0`. `Service.Basket()`
calls `recomputeTotals()`, which — on any session with a tax/
charge-policy asker installed (any country plugin, ADR-0061) — can
perform a **blocking plugin round-trip**, and this whole scan runs under
`SessionBasketManager.mu`. That's exactly the lock-scope class of bug
ut-docs#2443 (a different card, reviewed and merged the same day) just
closed on `BindTable`'s *bind* path — this diff would have silently
reintroduced it on the *busy-check* path instead: every table-QR scan
that finds any live session on that table would block every OTHER
table's guests behind a plugin ask, before even deciding busy/not-busy.
Fixed by switching the predicate to `len(sb.svc.Lines()) == 0` —
`Lines()` is a lock/copy/unlock with no recompute and no plugin call.
This also incidentally closes a nit the reviewer raised (`ItemCount()`
sums quantities, so a single line at fractional qty < 1 could read as
"empty" under the original predicate; `len(Lines()) == 0` matches the
doc comment's actual claim — "holding at least one line" — exactly).

**Two doc-staleness nits, fixed:** `BindTable`'s own struct-level lock-
order comment ("SetTable is the first MUTATING call") is true again with
the `Lines()` fix and needed no further edit; `TableOwnerActive`'s doc
comment claiming "the same recency window BindTable's busy check ...
uses" was stale (BindTable now uses two, item-count-dependent windows) —
reworded to point at BindTable's own doc rather than restate a claim that
could drift again.

**Two test-quality nits, fixed:**
`TestSessionBasketManager_BindTable_NonEmptySessionOutlastsEmptyMaxIdle`'s
comment overclaimed what it alone proves (it also passes against a
no-op mutation that never shortens anything — only the pairing with the
empty-side test proves the split); reworded to say so explicitly. No
page-level test asserted a NON-empty session still gets the LONG window
through the real production wiring — the reviewer empirically showed an
accidental swap of the two duration arguments at the `BindTable` call
site in `self_order_page.go` was caught by exactly one test (the
empty-side page test); added
`TestSelfOrder_NonEmptySessionStillBlocksPastEmptyMaxIdleWindow` so a
swap is caught by both page-level tests, not one.

All fixes re-verified after applying: `gofmt -l` clean, `go build`/`go
vet` clean, `golangci-lint run` 0 issues,
`go test ./internal/pos/... -race -run 'BindTable|SessionBasketManager' -v`
(15/15), `go test ./internal/pages/... -race -run 'SelfOrder' -v`
(63/63, including the two new/changed tests individually re-run), plus
the full unfiltered `go test ./internal/pages/... -race` (`make
test-race-pages`) as this pipeline's one full gate before commit.

## Base-branch merge (ut-docs#2443 landed mid-cycle)

`universal-till#1309` (ut-docs#2443's own fix, a separate card from the
same ut-docs#2434 review) merged into `main` while this fix was in
review, restructuring `BindTable` itself: the busy-scan loop now matches
on a new manager-owned `sessionBasket.tableID` field instead of
`sb.svc.TableID()`, specifically so the busy-check never depends on
`SetTable`'s own still-in-flight, unlocked write. Merging `main` into
this branch produced a real conflict in the busy-scan loop, resolved by
keeping `#2443`'s `sb.tableID` match and layering this card's
empty/non-empty window split on top of it — the `len(Lines()) == 0`
check this review's own S1 fix required stays correct and cheap under
the new structure for the same reason `TableOwner`/`TableOwnerActive`
already call `sb.svc.TableID()` under `m.mu`: it takes `Service.mu`
briefly but never waits on a plugin ask, unlike `Basket()`. Several test
call sites `#2443` added (`TestSessionBasketManager_BindTable_
SlowChargePolicyAskDoesNotBlockOtherTables`, `..._MintPathFactoryAsk
DoesNotHoldLock`, and setup lines in tests both cards touched) needed the
new two-duration signature and, in one case
(`..._NonEmptySessionOutlastsEmptyMaxIdle`'s setup), needed to bind
through `BindTable` itself rather than a direct `svc.SetTable` call —
the latter would leave `sb.tableID` unset under `#2443`'s new design and
silently break the test. Full `internal/pos` package (`-race`, all
tests, not just the touched ones) and the full `internal/pages` `-race`
gate (`make test-race-pages`) both re-run clean after the merge; see
Verdict.

## Confirmed clean, no action needed

- Backend-only diff (`git diff --stat`: 4 `.go` files) — no
  `web/ui/*.html`, no new user-facing copy/i18n key, no new modal/screen,
  so the UX-guidelines checklist and the help-manual check don't apply.
- No SQL outside `internal/data`/`internal/db` (`guard-data-access.sh`
  green); no self-order handler references the cashier's `Engine`
  (`guard-kiosk-engine.sh` green).
- No nil-panic risk (`Basket()`/`Lines()` both value-receiver, no nil
  `sb.svc` reachable — `TableID()` on the same struct already assumes
  it's non-nil).
- No new data race: `Lines()` returns a fresh copy under `s.mu`, safe to
  read after the manager's own lock is released.
- No secret-shaped literal; test data is generic (`table-A`, `T1`, `Flat
  White`), no real client/shop name.
- Argument order at the one production call site verified correct —
  empirically, not just by inspection (the reviewer's deliberate swap
  mutation failed the (new) page-level tests as expected).

## Deferred / not this card

- The reviewer's mitigating note that `HasItems()`
  (`internal/pos/session_manager.go`) still uses the same
  `Basket().ItemCount()` idiom this fix moved away from, under the same
  `m.mu`, for a different purpose (deciding whether an unattended update
  may proceed). Not touched here — out of this card's scope, and
  `HasItems` isn't on the hot per-scan busy-check path this card is
  about — but worth a human's call on whether it deserves the same
  `Lines()`-based fix as a separate follow-up.

## Verdict

Safe to merge. `docs/code-reviews/` record committed alongside the fix,
PR references `Closes universaltill/ut-docs#2444`.
