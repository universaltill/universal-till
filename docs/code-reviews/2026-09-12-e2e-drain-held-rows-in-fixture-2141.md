# Code review: drain held rows in the shared e2e fixture (ut-docs#2141)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2141
**Branch:** `fix/2141-e2e-drain-held-rows-in-fixture`
**Complexity:** medium (Dev: Sonnet inline, Review: Opus subagent in an isolated worktree)

## What shipped

The card named two `parked-orders-popup-2137.spec.ts` tests that hold a sale
and never resume it, leaking a held (parked) DB row to every later spec file
in the run — `POST /api/pos/reset` only clears the in-memory basket, never a
held row, so `fixtures.ts`'s existing per-file reset didn't touch it.

Investigating before scoping (per the `ba` skill) found the leak is broader
than the card describes: a repo-wide grep for "Hold Sale" surfaced several
more files that complete a hold and never resume it. Rather than patch each
test individually, the fix is systemic: `e2e/tests/fixtures.ts`'s existing
`resetPosOncePerFile` auto-fixture now drains held rows once per file (same
granularity as its existing basket reset), so every spec file starts clean
regardless of which earlier file leaked and regardless of whether that file
is ever touched again.

- `e2e/tests/helpers.ts`: `drainParkedOrders` refactored to take an
  `APIRequestContext` instead of a `Page` — it only ever made plain HTTP
  calls, and `fixtures.ts`'s auto-fixture has no `page` to give it.
- `e2e/tests/fixtures.ts`: `resetPosOncePerFile` now calls
  `drainParkedOrders(request)` instead of a bare `POST /api/pos/reset`.
- `e2e/tests/parked-orders-popup-2137.spec.ts`: its own file-level
  `afterEach` now also drains (closing the leak within this file too, not
  just for files that run after it), and its one pre-existing
  `drainParkedOrders(page)` call site was updated for the new signature.
- Two new files, `fixture-drain-2141-{a,b}-*.spec.ts`: a regression pair
  proving the cross-file drain for real — file A deliberately parks a sale
  and never resumes it, file B (a separate file, relying on this suite's
  already-established alphabetical file-sort order — see
  `playwright.config.ts`'s `AUTH_ONLY_SPECS` comment) asserts nothing is
  parked without draining itself.

TDD: RED confirmed against the pre-fix code (the new pair's file B fails with
the real held-row HTML in the assertion diff), GREEN after the fix. Re-verified
independently by Tester via `git stash`/`pop` on the actual fix files, not
just trusting the implementer's report.

## Independent review (Opus subagent, isolated worktree)

Ran the real gate (`guard-e2e-fixtures-import.sh`, the full `default`-project
suite twice — 456/456 both times — plus the `auth`/`ai-identify`/`layout`
projects), attempted to break the fix (reverted the drain call, confirmed the
regression pair fails the same way and CI's `retries: 1` doesn't save it),
and checked every call site of `drainParkedOrders` for the signature change.
No blockers. Found four real-but-minor issues, fixed here:

### 1. The regression pair's file A could pass with nothing actually parked

`fixture-drain-2141-a`'s only assertions were `#hold-modal` visible, then
hidden. But every `POST /api/pos/hold` failure path
(`internal/pages/hold_api.go`) still answers HTTP 200 with an error toast,
and the modal's own onclick closes on any successful HTTP response — not on
the till's actual verdict. The reviewer proved this live: a probe with an
**empty basket** passed the same two assertions with nothing ever parked. If
the park path ever broke, file A would stay green and file B would pass for
the wrong reason, silently retiring the regression guard.

**Fixed**: file A now also asserts the basket actually cleared (matching
`parkASale`'s own assertion in `parked-orders-popup-2137.spec.ts`) and that
a real held row exists via `GET /ui/parked-orders`, before handing off to
file B.

### 2. Stale/incorrect leak-site list in two comments

Both the new regression file's header comment and `fixtures.ts`'s own doc
comment claimed 7-8 leaking spots, naming `held-strip-scroll-affordance-2128
.spec.ts` and `settings-osk.spec.ts`. The reviewer checked live (a probe spec
reading `/ui/parked-orders` right after each file, no auto-drain) and found
neither actually leaks: the first already has its own `afterEach` cleanup,
the second's hold dialog is cancelled, never submitted (its own line-12
comment is about a basket line, not a held row). The real list is 5 files:
`hold-named-tab.spec.ts`, `new-sale-closes-payment-overlay-1386.spec.ts`,
`payment-overlay-duplicate-labels-1625.spec.ts`,
`payment-overlay-footer-reachable-1542.spec.ts`, `tender-panel-reachable
.spec.ts`, plus `parked-orders-popup-2137.spec.ts`'s own two tests.

**Fixed**: both comments corrected to name the real 5, with a note on the two
false positives and why they're false.

### 3. `drainParkedOrders` silently read a failed listing as "nothing parked"

The `GET /ui/parked-orders` call never checked response status. That endpoint
deliberately answers 500 on a repo read failure
(`internal/pages/open_orders_page.go`), and an unauthenticated caller on the
`auth` project gets redirected to `/login` — both parse as "no
`data-held-id`", i.e. silently clean. Pre-existing in the helper, but this
diff promotes it from one spec's own opt-in cleanup to the whole suite's
cleanliness contract on every file, where a silently-swallowed failure would
quietly reintroduce the exact order-dependent, CI-only failure this function
exists to prevent.

**Fixed**: throws with the status code if the listing response isn't 2xx.

### 4. `resetDoneForFile.add` ran before the drain it represents

`fixtures.ts` marked a file as "already reset" before `await
drainParkedOrders(request)` had actually completed. If the drain throws (an
unresumable row, or the listing failure above), the file would be
permanently marked done without ever having been drained.

**Fixed**: `resetDoneForFile.add(testInfo.file)` now runs only after the
drain succeeds.

Also applied two nitpicks from the same pass: `fixture-drain-2141-b` now uses
the top-level `request` fixture directly instead of `page.request` (it never
needed a browser page) and checks the listing's response status too, for the
same reason as #3; `fixtures.ts`'s comment now notes the drain is a no-op on
the `auth` project (no session cookie on the top-level `request` fixture,
same as the basket reset always was there) rather than reading as if every
project is protected.

## Verified beyond automated tests

- Full `default`-project e2e suite run **twice** (pre- and post-fix-of-
  findings), 456/456 both times, ~8.5 min each.
- `auth` + `ai-identify` + `layout` projects: 28/28 (reviewer's own run).
- `scripts/ci/guard-e2e-fixtures-import.sh`: green, 117 specs checked.
- `gofmt -l .` clean, `go build ./...` clean — no Go files touched.
- TDD RED→GREEN independently re-verified twice: once by Tester
  (`git stash`/`pop` on the fix files), once by the reviewer (reverted the
  drain call directly, confirmed the same failure, confirmed CI's
  `retries: 1` doesn't mask it).
- No real client/shop name added; existing demo-catalog barcode
  (`5000000000012` / "Coca-Cola") and "Task Runner"-style test data only.
- No Go/i18n/help changes — this is e2e test infrastructure only, no
  product behavior changed, so no ADR, no manual update, no locale keys.

## Deferred / explicitly out of scope

- The reviewer's "already-fine" note: `clearAllHeldSales` (used by
  `held-strip-scroll-affordance-2128.spec.ts`) and `drainParkedOrders`
  genuinely differ (the former also waits for the `#held-sales` htmx swap)
  and should NOT be consolidated — noted, not actioned, no card needed.
- Auditing e2e leaks outside the held-order class (e.g. basket/discount
  state) is unchanged — `fixtures.ts`'s existing basket reset already
  covers that; not this card's scope.

## Verdict

Safe to merge. All reviewer findings addressed; full suite green after
fixes.
