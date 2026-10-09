# Review — overlapping shell navigations: the later one wins (ut-docs#2869)

**Date:** 2026-10-09 · **Branch:** `fix/2869-shell-listener-race` ·
**Author:** Opus 5.5 (lane:cloud-41) · **Reviewer:** Fable (independent subagent)

## What the card said vs what reproduced

The card (found in the #2496 review) said two boosted navigations close
together leak the first page's window/document/body listeners, with the
final DOM correct. Reproducing it in real Chromium with real View
Transitions (Orders → tap Sell, tap Menu, both responses released back to
back) showed something worse:

- htmx 1.9.12 resolves `HX-Retarget: #ut-page` when each response arrives,
  but swaps inside the transition's update callback.
- A's update callback runs first (a skipped transition still runs it) and
  outerHTML-replaces `#ut-page`. B's swap then targets the detached old
  element and throws (`htmx:swapError` plus an uncaught page error).
- The till was left at URL `/menu` with `body.menu-screen` (B's shell sync,
  flushed by A's update callback because B's `beforeSwap` had overwritten
  the single `pendingSync` slot), but with Sell's content and listeners on
  screen.

## What shipped

`web/ui/layouts/base.html`:
- `htmx:beforeTransition` (when the transition will run and nothing else
  cancelled it) arms `{ superseded: false }` and adds it to `liveSwaps`.
  htmx calls `startViewTransition` synchronously right after, and the
  wrapper binds that entry to its own update callback.
- A swapping shell `htmx:beforeSwap` (past the signature/fallback gate)
  marks every live entry superseded.
- The update callback of a superseded swap returns `Promise.resolve()`
  without calling htmx's swap. The DOM goes straight from the old page to
  B, and B's target is still attached.
- The `pendingSync` reset moved below the fallback gate (review minor): a
  download fallback leaves the page in place, so an earlier swap that is
  still in flight keeps its own sync.
- The no-transition path (reduced motion / Light effects) is unchanged.
  There the swap is synchronous, so the race can't happen.

Tests:
- `e2e/tests/shell-listener-race-2869.spec.ts` has three tests:
  - a sequential control;
  - the deterministic guard: a `startViewTransition` stand-in queues update
    callbacks so B's `beforeSwap` lands before A's update callback;
  - a real-engine sanity check: real View Transitions, with both responses
    held by `page.route` and released back to back.
- `internal/pages/persistent_shell_test.go`: the wrapper's source-text
  guard is updated, and the new
  `TestPersistentShell_LaterNavigationSupersedesQueuedSwap` checks the arm,
  bind and supersede wiring.
- `web/help/img/manifest.json`: docs-shots surface hash only. No rendered
  pixel changes in steady state.

Docs: ut-docs ADR-0098 §7 gets a dated clarification (ut-docs PR on
`docs/2869-adr0098-overlapping-nav`).

## TDD, re-verified by the orchestrator

With `base.html` reverted to `main`:
- the deterministic race test fails with `TypeError: Cannot read
  properties of null (reading 'querySelector')` inside the queued swap;
- the real-VT test fails because `.menu-tile` is not found (Sell's content
  is on screen);
- the Go guards fail with all four messages.

With the fix, all of these pass. The targeted e2e set (this spec,
persistent-shell-2224, page-snapshot-no-jump-2496, page-slide-slow-swap-2496,
page-zoom-2942, effects-level-2859) passed 3× before the review fixes and 2×
after (29/29).

## Review findings (Fable)

No blockers, no majors. The reviewer traced htmx 1.9.12's
`handleAjaxResponse`. When the superseded swap is skipped, nothing stays
stuck:
- the request lock, `htmx-request` and `afterRequest` all happen in
  `onload`, before the swap;
- A's history push and title are correctly skipped;
- the settle promise is left pending, but nothing awaits it;
- the AbortError from the skip is absorbed by the wrapper's `.catch` and by
  `vtWatchdog`.

The outcome doesn't depend on which order the update callbacks run in, and
three overlapping navigations also work.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor (pre-existing) | `pendingSync` was reset at the top of `beforeSwap`, so a download fallback B dropped A's sync, and A then swapped in without `body.sale-screen` | **Fixed**: reset moved below the fallback gate |
| 2 | Nit | Arming ignored `e.defaultPrevented` from another `beforeTransition` listener: a tiny `liveSwaps` leak, but it could never skip a legitimate swap | **Fixed**: `if (e.defaultPrevented) return;` |
| 3 | Nit | The real-VT test can pass on `main` on a slow runner | **Fixed**: the spec header now says test 2 is the guard |
| 4 | Nit | `UT.zoomRemember` is still recorded for the superseded A | Accepted: harmless, overwritten on the next real visit |

## Verdict

Safe to merge.
