# Review — page slide cut short by the transition watchdog (ut-docs#2496)

**Date:** 2026-09-24 · **Branch:** `fix/2496-page-slide-watchdog` ·
**Author:** Opus 5.5 (lane:cloud-24) · **Reviewer:** Fable (independent, different model)

## What shipped

The product owner saw the iOS-style page/menu slide (ADR-0097) turn into a
different effect on the pilot tablet (v0.21.x).

**Root cause.** ADR-0098 (#2224, v0.19) moved shell navigation to boosted
htmx swaps, so the slide now runs as a *same-document* View Transition.
htmx creates that transition *before* it swaps the page in: the swap runs
inside the update callback. The 600 ms "never hold the screen" watchdog
was armed at creation time, so on a slow device the swap used most of the
budget, and the 200 ms slide was cut mid-way or skipped. Reproduced with
CDP CPU throttling on the unfixed `main` build:

| throttle | hop | update callback done | watchdog skip (before) |
|---|---|---|---|
| 4× | Menu→Items | 364 ms | skipped at 604 ms |
| 8× | Menu→Reports | 361 ms | skipped at 605 ms |
| 8× | Menu→Items | 670 ms | skipped at 1079 ms |
| 8× | Sell→Menu | 338 ms | skipped at 602 ms |

After the fix, the same runs show no skip at all (e.g. 8× Menu→Items: ready at
635 ms, finished at 1017 ms). At 1× nothing was ever skipped. That is why
desktop checks and CI never saw it.

**Fix** (`web/ui/layouts/base.html`): one shared `UT.vtWatchdog(vt)`,
defined before the pagereveal script's feature-check return:
- the 600 ms skip timer is armed on `vt.ready`, when the motion starts;
- a 2 s backstop from creation covers an engine whose `ready` never settles;
- both timers are cleared on `finished` (resolve or reject).

The pagereveal listener (cross-document) and the shell's
`startViewTransition` wrapper (boosted swap) both call it, so there is one
copy instead of two.

**Tests**
- `internal/pages/transitions_test.go` `TestBaseHTMLPageRevealHasASkipWatchdog`
  pins the helper's ready-armed timer, the backstop and both call sites,
  and that the helper comes before the early return. It also rejects the
  old creation-time timer shape.
- `e2e/tests/page-slide-slow-swap-2496.spec.ts`: a real boosted
  Menu→Reports hop in Chromium, with a 750 ms main-thread stall inside the
  transition's update callback (filtered to the `#ut-page` swap and proven
  to run inside it). It asserts that `ready` resolves, `ut-page-in` plays,
  the watchdog never skips, and the console stays clean.
- TDD verified by the author, revert→run→restore: the Go guard fails on
  the old `base.html`. The e2e spec fails on the old `base.html`
  (`skipped: true`) both before and after the review fixes, and passes 6/6
  and 5/5 repeated on the fix.

## Findings (Fable review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | The spec's `{once:true}` afterSwap stall could be spent on one of `/menu`'s load/poll chip swaps, which would make the test pass on unfixed code | **Fixed.** The stall only runs on the boosted `#ut-page` swap, after `networkidle`, and `stalledInside` asserts it ran inside the update callback. Re-verified red on old code. |
| 2 | minor | ADR-0097/0098 say "600 ms watchdog", but a never-ready transition can now hold the screen for up to 2 s | **Fixed.** Clarification notes added to both ADRs in ut-docs (branch `docs/2496-vt-watchdog-from-ready`). |
| 3 | minor | A slow CI runner hitting the 2 s backstop would fail with no clue why | **Fixed.** The assertion message carries the create→ready ms. |
| 4 | nit | A swap longer than 2 s trips the backstop just before `ready` | **Accepted and documented** in the comment (harmless: the swap held the screen, not the transition). |
| 5 | nit | The spec had no console watch | **Fixed** (`watchConsole`/`assertClean`). |

The reviewer confirmed from `htmx.min.js` 1.9.12 that the swap and
`htmx:afterSwap` run synchronously inside the `startViewTransition`
callback. It also confirmed the timer lifecycle has no leaks, the inline
scripts are `var`-only IIFEs, and the synthetic `pagereveal` in the 2223
spec is unaffected.

## Gate

`gofmt` clean, `go build ./...`, `go test ./...` ok, `golangci-lint` 0
issues, every `guard-*.sh` in `ci.yml`'s build job passes, and
`page-transitions-2223` + `persistent-shell-2224` + the new spec (21/21)
pass. The docs-shots surface hash was refreshed only: no rendered pixel
changes, since the docs-shots harness disables animations
(`Docs-Shots-Unchanged: true`). `shellcheck` is not installed in this
cloud container, and no `.sh` file changed.

## UX

Surfaces: every shell page hop (rail, Menu tiles, Sell↔Menu) on all device
modes. No markup, strings or CSS changed. The settled page was checked at
360 px and 1024×600 (screenshots looked at). The motion was verified by
the animation timeline (`ut-page-in`/`ut-page-out`, and the `-back`
variants on pop) running to `finished` under 4×/8× throttle.
**Not verified:** the physical pilot tablet. The product owner should
confirm the slide on the device after this ships.

**Verdict:** safe to merge.
