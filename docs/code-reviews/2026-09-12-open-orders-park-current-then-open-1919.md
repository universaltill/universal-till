# 2026-09-12 — Open orders: park-current-then-open instead of refusing a busy resume (ut-docs#1919)

## What shipped

`POST /api/pos/resume` and `/open-orders/resume` used to refuse to resume
a held order whenever the live basket already had items (`d.Engine.HasItems()`),
showing a "busy" toast and leaving the cashier stuck. Per the card's own
engineering decision (park-current-then-open, over a true two-basket
engine model), resuming now auto-parks the cashier's current in-progress
sale first — through the exact same code path a manual Hold takes — and
then loads the requested order, so switching orders never discards
whatever was already rung up.

- Extracted `parkCurrentBasket` (`internal/pages/hold_api.go`) out of the
  old `POST /api/pos/hold` handler body; both that handler and
  `resumeHeldSale` call it now.
- `resumeHeldSale` fetches and validates the TARGET row *before* touching
  the live basket, so an unknown/corrupt target never costs the cashier
  their in-progress sale.
- Removed the now-unreachable `resumeBusy` outcome and its two call sites.
- Added `resumeOKParkedPrior` and a new i18n toast
  (`hold.toast.parked_and_resumed`, all 4 core locales) so the cashier is
  told BOTH things happened on the sale-screen path, not just "resumed".
- Updated `web/help/{en,de,tr,fa,ar}/open-orders.md` (removed the "basket
  has to be empty first" instruction, now false) and doc comments
  referencing the old refusal.
- Removed the orphaned `hold.error.busy` key from all 4 core locales
  (nothing can produce it any more); the one test that used it as an
  arbitrary valid `?err=` key now uses `hold.error.not_found` instead.
- Replaced the two tests that asserted the old refusal with tests for the
  new auto-park behaviour, plus a new "target missing" guard test.

## Independent review

Reviewed by a fresh Opus subagent (complexity:medium → Sonnet builds,
Opus reviews), in an isolated git worktree, with its own revert-then-
restore TDD re-verification.

**Findings, and disposition:**

1. **Blocking, fixed (F1 — silent data loss).** Resuming the order the
   live basket **already is** (`HeldOrigin().ID == id`) fell through to
   the busy branch, auto-parking the live basket's CURRENT state into the
   very row about to be deleted (`Upsert`, same id), then deleting it —
   destroying anything added since a prior resume's `repo.Delete` had
   failed and left the row behind (a state `resumeHeldSale`'s own comment,
   and `HeldSalesRepo.Upsert`'s own doc comment, already anticipate as
   real). Pre-diff this path was harmless (`HasItems()` → refuse, nothing
   written); the diff turned an accepted-benign tradeoff into
   work-destroying. Fixed with a guard immediately after target
   validation: `HeldOrigin().ID == id` returns `resumeOK` as a no-op
   before ever reaching `HasItems()`. New regression test
   `TestResumeHandler_AlreadyLiveOrderIsANoOp` reproduces the stale-row
   precondition directly (re-inserts the row a failed `Delete` would have
   left) and asserts the basket and the row both survive untouched.
2. **Blocking, fixed (F2 — stale docs in 4 of 5 shipped languages).** Only
   `web/help/en/open-orders.md` had been updated; `de`/`tr`/`fa`/`ar`
   still instructed the removed "basket must be empty first" rule.
   `guard-help-drift.sh` only compares structure counts, so it could never
   catch prose that had gone false. Fixed by translating the same rewrite
   into all four; `guard-help-topics.sh`/`guard-help-drift.sh` both
   re-verified green (no new drift introduced on the `open-orders` topic).
3. **Non-blocking, deferred as ut-docs#2193 (F3 — feedback gap on one of
   two callers).** `resumeOKParkedPrior` reaches the new toast on the
   sale-screen fragment path (`POST /api/pos/resume`) but falls into
   `/open-orders/resume`'s `default:` redirect-to-`/` with no cue at all —
   that page has no existing success-notice mechanism (only `?err=` for
   failures). Not a data-loss risk (the auto-parked order is real,
   findable and resumable via the open-orders list itself); a
   discoverability gap on the exact surface (`ut-docs#2128`'s clipped
   strip) this feature's own reasoning is about. Filed as a follow-up
   rather than inventing new toast-over-redirect plumbing inside this
   diff's own scope.
4. **Non-blocking, fixed (F4 — orphaned i18n key).** `hold.error.busy`
   could no longer be produced by any code path once `resumeBusy` was
   removed, but the diff left it sitting in all 4 locale files plus one
   test using it as an arbitrary valid `?err=` key. Removed from every
   locale; the test switched to `hold.error.not_found` (a key the route
   can still actually produce), which pins the same generic banner
   mechanism the test is really about.
5. **Non-blocking, deferred as ut-docs#2194 (F5 — cosmetic label
   collision).** `parkCurrentBasket`'s clock-time label fallback
   (`15:04`, moved verbatim from the old Hold handler) now fires far more
   often — every auto-park with no customer name passes an empty
   `typedLabel`, where before only a manual Hold with nothing typed hit
   it. Two order-switches in the same clock-minute can display two rows
   with the identical label. IDs are `UnixNano`, so no collision or
   corruption — `/open-orders` still disambiguates by table/total/items/
   age — filed as a minor follow-up, not a merge gate.
6. **Non-blocking, fixed (test strength).** The original
   `TestResumeHandler_AutoParksBusyBasketThenResumes` asserted the
   auto-parked row's existence and id, never its content — it would have
   still passed if `parkCurrentBasket` wrote an empty/zeroed payload.
   Strengthened: the live sale now scans a second line (qty 2) before the
   busy resume, and the test asserts the restored basket is the target's
   own single-qty line while the auto-parked row's `line_count`/
   `total_minor` match the prior qty-2 sale's real content (240, not 0 and
   not the target's own 120).
7. **Verified clean, no change needed (things the review actively
   traced, not just reasoned about):**
   - `parkCurrentBasket`'s error paths (`json.Marshal`/`Upsert`/`Insert`)
     all return before `d.Engine.Reset()`, so a failure leaves the live
     basket completely untouched and both callers surface
     `hold.error.failed` — no path leaves the basket busy with the target
     never loaded.
   - Table-claim correctness through a park-then-restore inside one call:
     traced with a scratch scenario (live basket + table T1 claimed,
     resume target on T2) — T1 stays correctly claimed (the parked order
     still reads occupied, per `#1704`), T2 is correctly claimed, no
     double-release or leak. Mechanism: `parkCurrentBasket` deliberately
     doesn't release the claim (`#1704`), and `Engine.Reset()` clears the
     in-memory table pointer, so `prevTable` reads `""` at the point
     `resumeHeldSale` captures it — and releasing `""` is `releaseTableClaim`'s
     documented no-op. `#1381`/`#1918`'s existing regression tests
     (dine-in/takeaway preservation, stable identity across repark) all
     still pass.
   - No `os.MkdirAll`/`paths.Data(...)` class of bug — purely DB-backed,
     no file writes in either changed file.
   - No real client/shop name, no secret-shaped literal anywhere in the
     diff.
   - `reference/ux-guidelines.md` checklist: correctly N/A for everything
     layout/markup-shaped (no new templates, CSS, or controls — the new
     toast reuses the existing `.pos-notice` path); the one item worth
     actively checking (longest-locale overflow) confirmed clean — the
     new toast string is shorter than `hold.toast.held`, already rendered
     on the same surface, in every shipped locale.

**Deferred, not fixed:** ut-docs#2193 (F3, toast-on-redirect gap),
ut-docs#2194 (F5, cosmetic label collision) — both filed as follow-up
Backlog cards per the reasoning above.

**Found by real CI on the opened PR, fixed before merge (not caught by
the review pass above):**

8. **`playwright` (e2e) — a real, existing e2e spec still asserted the old
   refusal.** `e2e/tests/parked-orders-popup-2137.spec.ts`'s "a resume
   refused because the basket is busy closes the popup and says why"
   drove the exact busy-resume path through the real UI and asserted the
   old `hold.error.busy` toast text — a genuine Tester-step gap, since
   only the Go-level `internal/pages` tests were checked before pushing.
   Rewritten to `TestOpenOrdersResume`'s Go-level equivalent in Playwright
   form: taps a parked order while busy, asserts the tapped order loads
   with the new `hold.toast.parked_and_resumed` toast, and that the sale
   which WAS live is now its own separate parked entry (not the tapped
   order's row). Re-ran just this spec file locally (real server + real
   Chromium) after the fix — all 5 tests in the file pass.
9. **`build` job — `guard-docs-shots.sh`.** Changed `internal/pages/*.go`
   and 4 of the `open-orders` help topics without regenerating the
   manual's screenshots, per this repo's own standing rule (reviewer
   skill: "the matching topic under `web/help/` must be updated in the
   same branch... where the screen itself changed, the screenshot must
   have been regenerated"). Ran `make docs-shots`; the `open-orders`
   topic's own screenshots are pixel-identical (no UI/template changed,
   only the resume *behaviour* and help prose), so only the surface-hash
   manifest needed a bump. A handful of unrelated screenshots
   (catalog/sell/till-designer) also churned by a few bytes — this is
   `docs-shots`' known non-deterministic-regeneration noise (tracked
   separately, ut-docs#2184), not caused by this diff.

**Unrelated finding, filed separately:** `go test ./internal/pages/... -race`
reliably times out at 600s inside an unrelated pre-existing DB-migration
test (`TestSettingsDemoItemEndpoints_UnknownIDNeverElevates`), reproduced
identically before and after this diff — filed as ut-docs#2191, not
blocking this card (the full non-race suite and the targeted `-race` run
scoped to the changed tests both pass clean).

## Verified (beyond automated tests)

- `go build ./...` — clean.
- `go vet ./internal/pages/...` — clean.
- `go test ./internal/pages/... -run 'Hold|Resume|OpenOrders|ParkedOrders' -v`
  (both by dev and independently by the reviewer) — all green.
- `go test ./internal/pages/...` (full package, no `-race`, both by dev
  and independently by the reviewer) — green, ~235s.
- `go test ./internal/pages/... -run 'Hold|Resume|OpenOrders|ParkedOrders' -race`
  (scoped `-race` run, both by dev and independently by the reviewer) —
  green.
- `bash scripts/ci/guard-i18n.sh` — all locales match en.json, no
  duplicates, no orphaned literals.
- `bash scripts/ci/guard-help-topics.sh` — every topic parses, all
  shipped locales complete.
- `bash scripts/ci/guard-help-drift.sh` — green (pre-existing baselined
  drift only, none on the `open-orders` topic touched here).
- `bash scripts/ci/guard-data-access.sh` (reviewer) — no inline SQL
  outside `internal/data`/`internal/db`.
- Independent TDD re-verification (reviewer, in the isolated worktree):
  reverted the production files to pre-fix, kept the new tests — the two
  behaviour tests failed with the exact claimed pre-fix errors (busy
  refused, `Location` still `?err=hold.error.busy`); restored, all pass
  again. Separately confirmed the fetch-before-park reorder is genuinely
  pinned by temporarily moving the park above `repo.Get` and watching the
  "target missing" guard test fail for the right reason.

**`hold.toast.parked_and_resumed` is a brand-new core key** (doesn't
exist on any branch before this PR), so per this repo's own
`scrum-master`/reviewer-skill ordering rule, core merges first — `main`
goes red on `lang-pack-drift` until the two pack follow-ups land, which
is the expected, bounded state for a new key, not a mistake. Follow-up
PRs to `ut-plugin-language-de` and `ut-plugin-language-es` open and merge
immediately after this PR, in the same cycle.

**Verdict: safe to merge**, with F1 and F2 (from the pre-push review) and
findings 8-9 (from real CI on the opened PR) all fixed pre-merge. F1 was
a genuine data-loss bug; F2 and finding 9 were user-facing docs/manual
actively wrong or stale; finding 8 was a real gap in this diff's own
Tester-step coverage (an existing e2e spec exercising the exact behaviour
this card changed, missed because only the Go-level tests were checked
before the first push). All CI checks on the PR green after these fixes
(`ci`, `commit-attribution`, `lang-pack-drift`, `UI E2E`, `android-ci`).
F3/F5 deferred as tracked Backlog follow-ups; the pre-existing `-race`
package timeout is unrelated and tracked separately.
