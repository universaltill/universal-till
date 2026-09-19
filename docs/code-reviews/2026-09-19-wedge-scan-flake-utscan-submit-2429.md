# Code review: drive wedge-scan e2e test through window.utScan.submit() (ut-docs#2429)

**Date:** 2026-09-19
**Author (Dev):** Scrum Master pipeline, Sonnet, inline (complexity:easy)
**Reviewer:** independent Sonnet subagent, isolated worktree, fresh context
**PR:** universal-till (branch `fix/2429-wedge-scan-flake-utscan-submit`)

## What shipped

`e2e/tests/order-type-prompt-placement-2282.spec.ts`'s "before_item: a
wedge scan while the sale-start prompt is open is not lost" test
simulated a hardware barcode-scanner wedge with
`page.keyboard.type(code, { delay: 5 })` + `Enter`. Split from
ut-docs#2427's own review (see that card's own record) after it
reproduced a distinct failure mode: under CI-matching parallel load
(multiple Playwright workers + multiple Go till servers sharing a
runner), a single real inter-keystroke gap could exceed `app.js`'s own
100ms scan-buffer reset (`web/public/app.js:120`), truncating the
simulated barcode mid-burst. This produced a *stable* wrong result
("Item not found") rather than a slow render, so #2427's timeout/retry
headroom fix doesn't help this failure mode.

Fix: drive the test through `window.utScan.submit()`
(`web/public/app.js:167`), already exposed for the production
camera-scan integration (ut-docs#548) — a synchronous `page.evaluate`
call that sets the scan form's code input and triggers the exact same
`htmx.trigger(form, 'submit')` path a real wedge's buffered Enter
keystroke does, with no per-keystroke timing dependency at all. One
file, `e2e/tests/order-type-prompt-placement-2282.spec.ts`, +16/-2.

## What was verified beyond automated tests

Real browser, real Go till server:

- Full spec file, `--workers=1`: 11/11 passing (all sibling tests in the
  file, not just the changed one).
- The changed test alone under CI-matching contention,
  `--repeat-each=12 --workers=4` (4 parallel Playwright workers + 4 Go
  till servers on a 4-core sandbox — the exact load shape the card
  describes): 12/12 passing, ~3.3-3.9s each (vs. ~1.3s single-worker),
  confirming genuine host contention was created and the fix held up
  under it.
- `guard-e2e-fixtures-import.sh` passes. No Go/SQL/locale/migration
  files touched — test-infra-only change, so the rest of the
  `CLAUDE.md` "before committing" gate (`go build`, `go test`,
  `golangci-lint`, i18n/help-topic guards) is unaffected by construction
  and not separately re-run.

## Independent review findings

Verdict: **safe to merge, no blocking issues.** The reviewer
independently traced `scanCodeInput()`/`submit()` in `app.js`, confirmed
the scan form and order-type-prompt modal are both present in the
server-rendered initial markup (not htmx-lazy-loaded), confirmed
`submit()` dispatches the same `htmx:confirm`-intercepted event the
order-type-prompt gate cancels, and independently re-ran the suite
(11/11 full spec, 8/8 changed test under `--repeat-each=8 --workers=4`
in its own isolated worktree).

- **Folded in:** guard against `utScan.input()` finding no code field —
  `submit()` silently no-ops on a missing form/input, so without an
  explicit check a future selector regression could quietly turn this
  test into an always-green no-op (the "nothing landed" assertions would
  trivially still hold). Added `if (!codeInput) throw ...` before calling
  `submit()`.
- **Named, accepted as a documented trade-off, not fixed here:** the new
  path calls `submit()` directly rather than going through real
  keystrokes, so this specific test no longer exercises `app.js`'s
  window-level `keydown` buffer/reset logic "regardless of focus" for the
  *wedge* scenario — it only proves the app reacts correctly once
  `submit()` fires. That focus-independent buffering behavior stays
  covered elsewhere (`sale-screen-scan-focus-search-423.spec.ts`, via the
  existing `scanAtScannerSpeed` helper, ut-docs#2345), so this is a
  narrowing of *this* test's own intent, not a net coverage loss for the
  codebase. Reusing `scanAtScannerSpeed` itself here was considered and
  rejected: that helper `awaits` a `/api/pos/scan` network response,
  which never fires in this intercepted-by-modal scenario, so it doesn't
  fit this test's assertions.
- Confirmed `(window as any).utScan` follows the suite's existing
  precedent for calling window-level app hooks from `page.evaluate`
  (10+ other spec files use the same `(window as any)` pattern).

## Explicitly deferred

- The focus-independent-buffering coverage note above — not a gap, just
  documented here so a future reader doesn't mistake this test for still
  covering that axis.

## Safe-to-merge verdict

Yes. Test-infra-only change, no application code touched, the specific
flake mode is verifiably gone under the load pattern that produced it,
and the one real design trade-off found by review is accepted and
documented rather than silently introduced.
