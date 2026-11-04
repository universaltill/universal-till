# 2026-11-04 — unbounded-routes guard pairs Go handlers with app.js (ut-docs#5456)

**What shipped.** Test plus comments. `UT_UNBOUNDED_PATHS` in
`web/public/app.js` lists the routes that never get the 30 s client timeout
(ut-docs#5404). Before this change the list existed only in JS. The new
`TestUnboundedRoutesMatchAppJS` (`internal/pages/unbounded_routes_test.go`)
reads that list from app.js when it runs, and scans every
`Handle`/`HandleFunc` pattern registered in non-test Go files under
`internal/`. It checks:
(a) every `unboundedRoutes` entry is still registered;
(b) app.js exempts every declared route;
(c) every JS prefix covers at least one declared route;
(c2, added in review) no JS prefix also exempts a mutating route that is
neither declared nor listed in `alsoExempted`;
(d) a keyword net: any mutating route whose path looks slow must be
declared, covered, or listed in `timedOnPurpose` with a reason;
(e) the route scanner itself still works.
There are also stale-entry checks for `timedOnPurpose` and `alsoExempted`.
app.js gains three comment lines, and `docs/sale-screen-notifications.md`
gains one sentence. Runtime behaviour is unchanged.

**Pre-flight:** web/help topic: n/a (no shop-owner-visible change).
`make docs-shots`: n/a. reference/ page: `docs/sale-screen-notifications.md`
updated (now pointing to the test). README: n/a. en.json keys: n/a. Lang
packs: n/a. ADR: n/a. Review record: this file. Follow-ups: see Deferred.

**Review** (independent, Opus 5.5; Dev was Sonnet), in worktree
`/tmp/claude-0/wt-5456` on the WIP commit `a385edd`.

Findings:

- **F1 (Tester, medium) — fixed.** The keyword net in (d) only covered
  backup/restore/import/join/apply/pairing-restart/tender/refund/checkout,
  and only POST. A scratch `POST /api/xyz/export-all` passed.
  - Keywords: added `export|upload|update|install|restart`.
  - Methods: the net now covers every mutating method (anything except
    GET/HEAD; a pattern with no method counts as mutating). The till has no
    PUT/PATCH/DELETE routes today, so this only protects future ones.
  - 17 routes newly matched. I checked each one against app.js and its
    caller before classifying it:
    - fetch() callers: data/export, plugins store download/install,
      `plugins/{id}/update` (via `act()`), update/android-install,
      `/v1/install/*` stub.
    - `utPostWithElevation`: reports/archive/export.
    - No browser caller: install-from-marketplace.
    - `hx-trigger=load`, which is never timed: setup/update-check.
    - Server already bounds the call at 10 s: update/check
      (`updates.CheckNow`).
    - Fast: tills update-now (only queues the request), the setting/row
      saves, and catalog/item/update (htmx.ajax, not a user event).
  - All 17 went into `timedOnPurpose` with reasons. None needed to be
    unbounded.
- **F2 (Tester, medium) — fixed.** Check (c) accepted an over-broad prefix:
  `'/api/'` passed even though it would un-time every action. New check
  (c2): for each JS prefix, every mutating registered route under it must be
  in `unboundedRoutes` or in the new `alsoExempted` map. It reports one error
  per prefix, with a count and the first five routes. It found one real
  side effect: `POST /api/refund/preview` sits under `/api/refund`. That
  route only recomputes a total and is harmless left untimed, so it went
  into `alsoExempted`. I did not add a separate "too-short prefix" rule,
  because (c2) already covers it.
- **Minor (Tester) — accepted.** The pairing-restart rule is checked by
  searching for a literal string, so a copy of
  `/\/pairing-restart$/.test(path)` inside a JS comment would hide its
  removal. Low risk.
- **Minor (reviewer) — accepted.** `registeredRe` also matches a
  `HandleFunc("…")` that appears inside a Go comment. A route that was
  commented out would then still count as registered, and check (a) would
  not flag it as stale. Low risk: the Tester's grep found no such
  comment-only registrations.
- Tables re-checked: all 19 `unboundedRoutes` patterns are registered. The
  reasons match the handlers. `covered()` behaves like `utIsUnboundedPath`
  (prefix `indexOf===0` or a `/pairing-restart$` suffix). Doc sentence
  accurate.

**Extra verification (reviewer, revert→run→restore in one turn each):**

- Added `'/api/'` to `UT_UNBOUNDED_PATHS` → FAIL: `UT_UNBOUNDED_PATHS entry
  "/api/" … also exempts 280 route(s) not declared long-running
  (/api/buttons/add, …)`. Restored → ok.
- Scratch package with `POST /api/xyz/export-all` and `PUT /api/xyz/install`
  → FAIL: `new long-running-looking route "POST /api/xyz/export-all"` and
  the same message for `"PUT /api/xyz/install"`. Both passed before the fix.
  Scratch removed → ok.
- Tester's run: N1–N6 negatives and their own negative all failed with the
  claimed messages. e2e `tests/action-timeout-5404.spec.ts` gave **9
  passed** (47 s).
- Gates after the fixes: `gofmt -l .` empty; `go vet ./internal/pages/` ok;
  `go test ./internal/pages/ -run TestUnboundedRoutesMatchAppJS -count=2 -v`
  PASS ×2; `golangci-lint run ./internal/pages/...` 0 issues;
  `guard-i18n.sh` rc=0; `guard-rtl-logical-css.sh` rc=0.

**Verdict:** approve with the F1/F2 fixes above (uncommitted in the worktree).

**Deferred:**

- Proposed card: "app.js: comment-proof the pairing-restart marker check in
  TestUnboundedRoutesMatchAppJS". Optional; only if the marker ever moves.
- `utPostWithElevation` legs time out on the same 30 s. reset-archives
  restore and reports/archive/export rely on that; already tracked on
  ut-docs#5445/#5454, so no new card.

## Addendum: main's move-approval time bomb, fixed in this PR

CI on this PR went red on `pages-shuffle (seed 22)` with every
`internal/pages/move_approval_5127_test.go` test failing ("the pending move
is not held", "This move is no longer waiting for approval"). It isn't the
new guard (it runs later in that order) and it isn't test order:

- The fixture's `mvExp` was the fixed `2026-11-03T09:30:00Z`.
- The pages handlers read the real clock (`enroll.moveNow = time.Now`), and
  `PendingMoves` drops a move 24 h after it expires (`pendingMoveGrace`).
- So from 2026-11-04T09:30Z every move test fails on `main` too.

The fix makes `mvExp` a package var, now + 6 h at second precision in
RFC3339. All three uses (the signed approval, the stub cloud's reply,
`ApplySyncMoves`) read the same string once. CI then showed
`enroll/move_5127_test.go`'s fixed `moveExp` has the same bomb: only the
MoveCapable TTL test stubs `moveNow`, and 11 tests failed on `main`. It got
the same fix (approved by the same reviewer), and `go test -race
./internal/enroll/` passes.

The docs-shots surface hash was refreshed with
`update-docs-shots-surface-hash.sh`, because every surface change here is
a test file or a JS comment (`Docs-Shots-Unchanged: true`).

- **Author:** Opus.
- **Review:** independent, Sonnet, read-only. Approved, with no upper bound
  on `expires_at` anywhere (`parsePendingMove`).
- **Verified:** the move tests pass, and the full package passes with
  `-shuffle=22`. The failure reproduced locally before the fix.
- **Deferred:** other fixed dates feed real-clock checks:
  - `subscription_check_test.go:52`: 2026-11-14
  - `entitlement_api_test.go:91,99`: 2026-12-31
  - `enroll/replica_test.go:514,535`: 2026-12-31

  They're filed as a follow-up card.
