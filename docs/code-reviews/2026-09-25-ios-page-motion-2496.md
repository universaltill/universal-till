# Code review — iOS-style page motion and the pre-snapshot layout jump (ut-docs#2496, reopened)

- **Date:** 2026-09-25
- **Branch:** `fix/2496-ios-page-motion`
- **Decision:** ut-docs ADR-0118 (amends ADR-0097 §4, ADR-0098 §7)
- **Author:** Opus 5.5 dev subagent; **reviewer:** Fable (independent, different model)

## What shipped

1. **Root cause of "the lower part disappears, the page goes down, then it changes".**
   The boosted-navigation `htmx:beforeSwap` hook in `web/ui/layouts/base.html`
   copied the response's `<body>` classes and `<html>` attributes onto the live
   document. htmx 1.9.12 fires `beforeSwap` *before* it calls
   `document.startViewTransition`, so leaving Sell dropped `body.sale-screen`
   (`height: 100dvh`, full-height flex) under the outgoing page. It grew from
   600 px to 2043 px before its snapshot, and that collapsed picture is what slid out.
   The sync is now stashed (`pendingSync`) and applied once, at the start of the
   View Transition update callback (the `startViewTransition` wrapper wraps both
   call forms), or on `htmx:afterSwap` when no transition runs.
2. **Motion (ADR-0118):** 400 ms, `cubic-bezier(.32,.72,0,1)`. On push the new page
   slides in full width on top while the old page recedes (scale .92, +3 % Y,
   opacity .55) over a black backdrop. Pop is the reverse, with the old page on top.
   It uses transform and opacity only, and RTL goes through `--ut-nav-dir`. The rail and
   status bar stay fixed, and reduced motion still kills all of it.
3. **Input never waits:** `::view-transition { pointer-events: none }`. Chromium 141
   ignores that rule (it still hit-tests `<html>`; the dev found this and the
   reviewer re-confirmed it independently), so there is a JS fallback: a
   `pointerdown` during a transition skips it, and a click already aimed at
   `<html>` is re-dispatched to the element under the pointer.
4. **Watchdog:** 1000 ms after `vt.ready` (was 600), with the 2 s creation backstop kept.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | minor | A mouse click on a field mid-transition never focused it: the trusted mousedown also went to `<html>`. This affects the Windows shell (scan field). | **Fixed.** The hand-over focuses the closest focusable control first. New e2e test "a mouse click on a field during the transition focuses it" fails without the fix (`Expected "code", Received null`) and passes with it. Also covered by a Go guard. |
| M2 | minor | A swap that throws leaves `pendingSync` set, so a later, unrelated transition could flush it. | **Fixed.** Cleared at the top of the shell `beforeSwap` branch and on `htmx:swapError`. Go guard added. |
| M3 | minor | `::view-transition { background: #000 }` puts a colour in the motion block, where ADR-0097 said "colour-free". | **Accepted.** An iOS-style black backdrop is deliberate, and ADR-0118 records it. It is theme-independent, like the platform's own. |
| N1 | nit | A tap in the one frame before the update callback lands on the old page. | Accepted: that is also what the screen shows at that moment. |
| N2 | nit | Long-press and the OSK see `<html>` as the pointerdown target mid-transition. | Accepted: rare and touch-safe. |
| N3 | nit | Two boosted responses before the first update callback can leak page A's listeners. | Pre-existing on `main`. Filed as a Backlog card. |
| N4 | nit | The 1 s `skippedByTapAt` window. | Harmless (reviewer's analysis). |

## Verified

- **TDD:** the reviewer checked out `origin/main`'s `base.html` and `app.css`. All five new or changed Go guards fail on it and pass on the fix. The new snapshot spec fails on `main` with the measured 600 → 2043 px collapse. The M1 e2e test was re-verified by reverting the focus line.
- **Gate:** gofmt clean, `go build`, `go test ./...`, golangci-lint 0 issues. All `ci.yml` build-job guards pass except `guard-shellcheck-version` (no shellcheck binary here; no `.sh` changed) and `guard-deadcode-baseline` (it skips `cmd/unitill-desktop` without GTK headers, and it fails identically on the unchanged tree). Full e2e: 694 passed (dev run); the affected specs were re-run after the review fixes: 5 of 5 passed.
- **Reviewer probes:** a mid-transition click boosts exactly once (no double activation). The reduced-motion path still syncs the shell.
- **Visual:** frames at 0/100/200/300/400 ms, 1024×600 and 360 px, default and dark themes, plus a 4× CPU-throttled screencast. The 0 ms frame shows the intact Sell layout. The old page recedes and the new one slides over it. The rail and status bar stay fixed. The dark theme does not flash.
- **Docs shots:** no pixel of a settled page changed (the dev ran two `make docs-shots` runs, identical); the surface hash was refreshed with a `Docs-Shots-Unchanged` trailer.

## Not verified

- Feel on the real pilot tablet (Android WebView), on Windows (WebView2) and on the Pi (WebKitGTK), including whether those engines honour `::view-transition { pointer-events: none }`. ADR-0118 assigns this to the product owner.
- The cross-document fallback path: headless Chromium doesn't run it. It uses the same keyframes.

**Verdict:** safe to merge.
