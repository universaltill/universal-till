# Code review — gift voucher popup sticky row background regression test (ut-docs#5310)

Date: 2026-11-03 · Lane: lane:cloud-24 · Complexity: easy

## What shipped

`e2e/tests/gift-voucher-sticky-bg-5310.spec.ts`, a regression test only. The CSS fix
(`.gift-voucher-popup .modifier-actions { background: Canvas }`) already landed
on `main` in 963fe74 (ut-docs#5304). Before it, dark.css's `--surface`
(`#111827`) painted a bluish strip under the dialog's UA `Canvas` (rgb(18,18,18)).
The card's remaining acceptance criterion was a computed-style assertion.

The spec opens the popup through the real search-and-tap path, in the default
theme and in dark.css. The dark theme is swapped in via `#theme-css`, as
receipt-theme-contrast-4419 does, and the spec awaits `onload`. It asserts that the
sticky row's computed `background-color` is not transparent and equals
`#modifier-modal`'s. A non-vacuity guard checks that dark is really applied
(`color-scheme` dark, `--surface` == `#111827`).

## Process

Dev: Sonnet subagent. Tester: separate subagent. Reviewer: Opus 5.5 subagent in
its own worktree (author ≠ reviewer). BA and Architect were done by the
orchestrator `[no-subagent: one-file test backfill, fix already on main]`.

## Verification

- Green: 5310 (default + dark) and gift-voucher-item-4214 (x2) give 4 passed. Dev,
  Tester and Reviewer each ran them independently.
- Red: with only the gift-voucher rule (app.css ~4448) reverted to
  `background: var(--surface)`, the dark test fails: `Expected "rgb(18, 18, 18)",
  Received "rgb(17, 24, 39)"`. The Tester also made it fail with `#222`, which
  fails both tests. app.css was restored each time (empty diff).
- Visual: the Tester took a 1024x600 dark screenshot of the open popup. It shows no
  strip behind Cancel/Add.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | `openPopup` and the create/deactivate hooks duplicate gift-voucher-item-4214 | accepted. Two small specs; extract to `helpers.ts` if a third copy appears |
| 2 | low | no `watchConsole(page)` (4214 has it) | accepted. This spec checks styles only; 4214 covers console errors on the same popup flow |
| 3 | info | the default-theme case can't catch this regression (light `--surface` == `Canvas`) | expected; the dark case is the guard |

Supersession: there's no existing test on `main` for this row's background
(`git grep` on e2e). The pin-dialog-5304 spec covers only the PIN dialog.

Pre-flight: web/help topic + docs-shots n/a (test-only, no user-visible change; fix shipped in 963fe74/#5304); reference/ n/a; README n/a; en.json/meta/locales/lang packs n/a (no keys); ADR n/a; plugin/manifest mirrors n/a; review record updated (this file); follow-ups none.

Verdict: approve.
